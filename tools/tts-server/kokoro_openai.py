"""Kokoro-82M → OpenAI 兼容的 TTS 网关（免费、可商用）。

为什么选它
    模型只有 82M，CPU 能跑到实时；中文有 v1.1-zh 专用权重；
    代码与权重都是 Apache-2.0（可商用，保留声明即可）。
    对照：edge-tts 没有商用授权；MiniMax 要付费；Piper 的音色授权要逐个核实。

用法
    set HF_ENDPOINT=https://hf-mirror.com     # 国内直连 HuggingFace 会超时
    python tools/tts-server/kokoro_openai.py --port 9884

    后台 → AI 主播 → 配置 → 语音：
        通道     = openai
        服务地址 = http://127.0.0.1:9884/v1
        音色     = zf_xiaobei（页面点「拉取」有下拉）

音色
    /v1/voices 会列出来。中文（lang_code=z）：
      zf_xiaobei  zf_xiaoni  zf_xiaoxiao  zf_xiaoyi
      zm_yunjian  zm_yunxi   zm_yunxia    zm_yunyang
"""

from __future__ import annotations

import argparse
import io
import os
import threading
import time

import numpy as np
import soundfile as sf
from fastapi import FastAPI, HTTPException
from fastapi.responses import Response
from pydantic import BaseModel

SAMPLE_RATE = 24000

# 中文音色（Kokoro v1.1-zh）。名称沿用 edge 那套习惯，方便和后台下拉对应。
ZH_VOICES = [
    ("zf_xiaobei", "晓北 · 女 · 温柔"),
    ("zf_xiaoni", "晓妮 · 女 · 活泼"),
    ("zf_xiaoxiao", "晓晓 · 女 · 亲切"),
    ("zf_xiaoyi", "晓伊 · 女 · 卡通"),
    ("zm_yunjian", "云健 · 男 · 沉稳"),
    ("zm_yunxi", "云希 · 男 · 阳光"),
    ("zm_yunxia", "云夏 · 男 · 少年"),
    ("zm_yunyang", "云扬 · 男 · 播音"),
]
ZH_IDS = {v[0] for v in ZH_VOICES}

app = FastAPI(title="dafeiyu TTS gateway (Kokoro)")

# KPipeline 不是线程安全的（底层 torch 推理），一次只让一句进去。
_lock = threading.RLock()
# _synth 单句合成锁：排太久直接 429，不让请求堆到 30 秒后一起超时。
_synth = threading.Lock()
_pipes: dict[str, object] = {}


def pipeline_for(voice: str):
    """按音色前缀选语言：zf_/zm_ → 中文，其余按首字母映射到 Kokoro 的语言码。"""
    if voice in ZH_IDS:
        lang = "z"
    else:
        lang = (voice[:1] or "a").lower()
        if lang not in {"a", "b", "e", "f", "h", "i", "j", "p", "z"}:
            lang = "z"
    with _lock:
        if lang in _pipes:
            return _pipes[lang]
        from kokoro import KPipeline

        # KOKORO_DEVICE=cpu 可强制回 CPU（显存被 Ollama 吃满时的退路）；
        # 不填则交给 KPipeline 自动选（cuda 可用就用 cuda）。
        dev = os.getenv("KOKORO_DEVICE", "").strip() or None
        p = KPipeline(lang_code=lang, device=dev)
        _pipes[lang] = p
        return p


class SpeechReq(BaseModel):
    # 字段名照抄 OpenAI：backend 只发 model/input/voice/speed。
    model: str = ""
    input: str = ""
    voice: str = ""
    speed: float = 1.0
    response_format: str = "wav"


@app.get("/health")
def health():
    return {
        "ok": True,
        "engine": "kokoro",
        "model": "Kokoro-82M",
        "license": "Apache-2.0",
        "loaded": list(_pipes.keys()),
        "voices": [v[0] for v in ZH_VOICES],
    }


@app.get("/v1/voices")
def voices():
    return {
        "engine": "kokoro",
        "license": "Kokoro-82M · Apache-2.0（代码与权重均可商用，保留声明）",
        "voices": [
            {"id": vid, "name": name, "note": "免费商用 · Kokoro-82M", "commercial": True}
            for vid, name in ZH_VOICES
        ],
    }


@app.post("/v1/audio/speech")
def speech(req: SpeechReq):
    text = (req.input or "").strip()
    if not text:
        raise HTTPException(400, "input 不能为空")
    voice = (req.voice or ZH_VOICES[0][0]).strip()
    speed = req.speed if req.speed and req.speed > 0 else 1.0
    if not _synth.acquire(timeout=20):
        raise HTTPException(429, "上一句还在合成，稍后再试")
    try:
        try:
            t0 = time.time()
            pipe = pipeline_for(voice)
            chunks = [a for _, _, a in pipe(text, voice=voice, speed=speed)]
            print(f"[kokoro] synth {len(text)} chars in {int((time.time() - t0) * 1000)}ms", flush=True)
        except Exception as e:  # noqa: BLE001 - 把真实原因原样回给后台，别让页面只看到 500
            # 显存被 Ollama 吃满时，第一次 CUDA 分配会 OOM。自动降级到 CPU 重试一次，
            # 宁可慢一点，也不要让主播突然哑掉。
            if "out of memory" not in str(e).lower():
                raise HTTPException(502, f"Kokoro 合成失败（{voice}）：{e}")
            try:
                import torch

                torch.cuda.empty_cache()
            except Exception:
                pass
            _pipes.clear()
            os.environ["KOKORO_DEVICE"] = "cpu"
            pipe = pipeline_for(voice)
            chunks = [a for _, _, a in pipe(text, voice=voice, speed=speed)]
            print("[kokoro] CUDA OOM，已降级到 CPU 重试成功", flush=True)
    finally:
        _synth.release()

    audio = np.concatenate(chunks) if chunks else np.array([], dtype=np.float32)
    if audio.size == 0:
        raise HTTPException(502, "Kokoro 返回空音频（检查音色 ID）")

    buf = io.BytesIO()
    sf.write(buf, audio, SAMPLE_RATE, format="WAV", subtype="PCM_16")
    return Response(content=buf.getvalue(), media_type="audio/wav")


if __name__ == "__main__":
    import uvicorn

    ap = argparse.ArgumentParser()
    ap.add_argument("--host", default="127.0.0.1")
    ap.add_argument("--port", type=int, default=9884)
    a = ap.parse_args()
    # 启动即预热：模型加载要 30 秒左右（CPU），不预热的话第一句会等这么久，
    # 而直播里的第一句恰恰是观众第一次听到它的时候。
    try:
        print("[kokoro] loading pipeline ...", flush=True)
        pipe = pipeline_for(ZH_VOICES[0][0])
        # 预热一次 CUDA：不预热时第一个真实请求要 8 秒（内核初始化），
        # 而那句话往往正是观众第一次听它开口。
        list(pipe("你好", voice=ZH_VOICES[0][0]))
        print("[kokoro] ready (warmed)", flush=True)
    except Exception as e:  # noqa: BLE001 - 预热失败也要把 /health 开起来，方便看原因
        print(f"[kokoro] preload failed: {e}", flush=True)
    uvicorn.run(app, host=a.host, port=a.port, log_level="info")
