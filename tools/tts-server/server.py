"""本地 TTS 服务：把 Piper 包成 OpenAI 兼容的 /v1/audio/speech。

为什么要有这一层
    backend 的 modules/ai/tts 已经把「OpenAI 兼容端点优先」当成默认路径
    （POST {base}/audio/speech，见 tts.go 的 KindOpenAI），所以本地引擎只要
    长得像 OpenAI，Go 端一行都不用改，换音色只是改一条配置。

为什么选 Piper
    MIT 授权（可商用）、纯 ONNX（CPU 实时，不依赖 CUDA）、Windows 有现成 wheel。
    实测中文 20 字合成 1.81s、音频 3.18s、RTF 0.57（比实时快）。
    对照：edge-tts 无商用授权；CosyVoice2 要 GPU 才有可用的直播延迟，
    而这台机器的 torch 是 CPU 版。

用法
    python tools/tts-server/server.py --port 9880
    后台 → AI 主播 → 配置 → 语音：
        通道   = openai
        服务地址 = http://127.0.0.1:9880/v1
        音色   = zh_CN-huayan-medium（或 /v1/voices 里的任意一个）

模型放哪
    data/tts/models/*.onnx（同名 .onnx.json 必须在一起）。
    多放一个 .onnx 就多一个音色，重启服务即可见。
"""

from __future__ import annotations

import argparse
import io
import threading
import wave
from pathlib import Path

from fastapi import FastAPI, HTTPException
from fastapi.responses import Response
from pydantic import BaseModel

ROOT = Path(__file__).resolve().parents[2]
MODEL_DIR = ROOT / "data" / "tts" / "models"

# Piper 的 onnxruntime session 不是线程安全的，一次只让一句进去；
# 反正主播本来就"一句说完再说下一句"，串行不影响体感。
_lock = threading.RLock()
_voices: dict[str, object] = {}


def model_files() -> list[Path]:
    return sorted(MODEL_DIR.glob("*.onnx"))


def voice_id(p: Path) -> str:
    return p.stem


def voice_ids() -> list[str]:
    return [voice_id(p) for p in model_files()]


def get_voice(name: str):
    """按音色名取（懒加载 + 缓存）。名字就是 .onnx 的文件名去扩展名。"""
    from piper import PiperVoice

    with _lock:
        if name in _voices:
            return _voices[name]
        fname = name if name.endswith(".onnx") else name + ".onnx"
        path = MODEL_DIR / fname
        if not path.exists():
            have = "、".join(voice_ids()) or "（空）"
            raise HTTPException(400, f"找不到音色 {name}；{MODEL_DIR} 下只有：{have}")
        v = PiperVoice.load(path)
        _voices[name] = v
        return v


def synth(name: str, text: str, speed: float) -> bytes:
    from piper import SynthesisConfig

    with _lock:
        v = get_voice(name)
        # speed 是倍率（1=原速），Piper 用的是 时长缩放 length_scale（越小越快），互为倒数。
        length_scale = 1.0 / speed if speed and speed > 0 else 1.0
        cfg = SynthesisConfig(length_scale=length_scale)
        buf = io.BytesIO()
        with wave.open(buf, "wb") as w:
            v.synthesize_wav(text, w, syn_config=cfg)
        return buf.getvalue()


app = FastAPI(title="dafeiyu local TTS (Piper)")


class SpeechReq(BaseModel):
    # 字段名照抄 OpenAI，多余字段一概忽略：后端只会发 model/input/voice/speed。
    model: str = "piper"
    input: str = ""
    voice: str = ""
    speed: float = 1.0
    response_format: str = "wav"


@app.get("/health")
def health():
    return {
        "ok": True,
        "engine": "piper",
        "modelDir": str(MODEL_DIR),
        "voices": voice_ids(),
        "loaded": list(_voices.keys()),
    }


@app.get("/v1/voices")
def voices():
    # commercial 故意不写 true：Piper 引擎本身是 MIT，但每个音色模型的训练数据授权
    # 各不相同（HuggingFace 上要逐个看模型页）。页面靠这个字段标“商用已确认/自行核实”，
    # 乱填 true 比空着更坑人。
    return {
        "engine": "piper",
        "license": "Piper 引擎 MIT；音色模型授权以各自模型页为准",
        "voices": [
            {
                "id": voice_id(p),
                "name": voice_id(p),
                "note": "本地 Piper · 离线 · 引擎 MIT（模型授权自行核实）",
                "commercial": False,
            }
            for p in model_files()
        ],
    }


@app.post("/v1/audio/speech")
def speech(req: SpeechReq):
    text = (req.input or "").strip()
    if not text:
        raise HTTPException(400, "input 不能为空")
    have = voice_ids()
    if not have:
        raise HTTPException(503, f"{MODEL_DIR} 下没有 .onnx 模型，先放一个再启动")
    name = req.voice.strip() or have[0]
    audio = synth(name, text, req.speed)
    return Response(content=audio, media_type="audio/wav")


if __name__ == "__main__":
    import uvicorn

    ap = argparse.ArgumentParser()
    ap.add_argument("--host", default="127.0.0.1")
    ap.add_argument("--port", type=int, default=9880)
    a = ap.parse_args()
    uvicorn.run(app, host=a.host, port=a.port, log_level="info")
