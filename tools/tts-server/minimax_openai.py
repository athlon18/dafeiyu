"""MiniMax → OpenAI 兼容的 TTS 网关。

为什么要这一层
    backend 的 modules/ai/tts 只认 OpenAI 形状：POST {base}/audio/speech。
    MiniMax 的同步语音合成是 POST /v1/t2a_v2，字段名不同、返回的还是 hex 编码音频。
    在中间放一个几十行的翻译插件，就能让 MiniMax 全部系统音色直接出现在后台的
    「音色」输入框里——业务代码一行都不用改。

为什么选 MiniMax 而不是本地开源
    要的是「日系软萌」。本地只有两条路，都不通：
      - Piper：中文仅一个 zh_CN-huayan 标准女声，做不出软萌；
      - CosyVoice2：Windows 卡在 pynini（无 wheel，要本地编 OpenFst），且需要 GPU
        （本机 torch 是 2.5.1+cpu）。
    MiniMax 的系统音色里直接有「嗲嗲学妹 / 俏皮萌妹 / 柔和少女」，且是正规商用授权。

用法
    set MINIMAX_API_KEY=你的key
    python tools/tts-server/minimax_openai.py --port 9883

    后台 → AI 主播 → 配置 → 语音：
        通道     = openai
        服务地址 = http://127.0.0.1:9883/v1
        音色     = diadia_xuemei      （页面点「拉取」就有下拉，不用记 ID）
        模型     = speech-02-hd
        API Key  = 你的 MiniMax Key（随请求带给网关；也可给网关设 MINIMAX_API_KEY）

    软萌音色（从软到不太软）：
        diadia_xuemei                   嗲嗲学妹   ← 最贴「日系软萌」
        qiaopi_mengmei                  俏皮萌妹
        Chinese (Mandarin)_Soft_Girl    柔和少女
        Chinese (Mandarin)_Cute_Spirit  憨憨萌兽
        female-shaonv-jingpin           少女音色（beta）
        Chinese (Mandarin)_Crisp_Girl   清脆少女
        Chinese (Mandarin)_Warm_Girl    温暖少女
        female-tianmei-jingpin          甜美女性（beta）

    想更软一点：调 speed 到 0.95 左右，或把 MINIMAX_TTS_MODEL 换成 speech-02-hd。
"""

from __future__ import annotations

import argparse
import json
import os
import urllib.error
import urllib.request

from fastapi import FastAPI, HTTPException, Request
from fastapi.responses import Response
from pydantic import BaseModel

# 国内站；海外站是 api.minimax.io，可用 MINIMAX_API_BASE 覆盖
DEFAULT_API = "https://api.minimaxi.com/v1/t2a_v2"
DEFAULT_VOICE = "diadia_xuemei"


app = FastAPI(title="dafeiyu TTS gateway (MiniMax)")


class SpeechReq(BaseModel):
    # 字段名照抄 OpenAI：backend 只会发 model/input/voice/speed，其余忽略。
    model: str = ""
    input: str = ""
    voice: str = ""
    speed: float = 1.0
    response_format: str = "mp3"


def _post(url: str, payload: dict, key: str) -> dict:
    body = json.dumps(payload, ensure_ascii=False).encode("utf-8")
    req = urllib.request.Request(
        url,
        data=body,
        headers={
            "Authorization": "Bearer " + key,
            "Content-Type": "application/json",
        },
        method="POST",
    )
    try:
        with urllib.request.urlopen(req, timeout=60) as r:
            return json.loads(r.read().decode("utf-8"))
    except urllib.error.HTTPError as e:
        detail = e.read().decode("utf-8", "ignore")[:300]
        raise HTTPException(502, f"MiniMax HTTP {e.code}: {detail}")
    except urllib.error.URLError as e:
        raise HTTPException(502, f"连不上 MiniMax: {e.reason}")


@app.get("/health")
def health():
    key = os.getenv("MINIMAX_API_KEY", "").strip()
    return {
        "ok": bool(key),
        "engine": "minimax",
        "hasKey": bool(key),
        "model": os.getenv("MINIMAX_TTS_MODEL", "speech-02-hd"),
        "defaultVoice": DEFAULT_VOICE,
    }


# 软萌音色表：从 MiniMax 官方系统音色里挑出来的，顺序从“最软”到“不太软”。
# 放这里的目的：配置页的「拉取音色」直接拿到带说明的下拉，不用再去翻文档抄 ID。
# commercial=True 的依据是 MiniMax 开放平台对 API 付费使用授予商用；具体条款以你签的为准。
SOFT_VOICES = [
    ("diadia_xuemei", "嗲嗲学妹", "最贴日系软萌 · 女声"),
    ("qiaopi_mengmei", "俏皮萌妹", "俏皮 · 女声"),
    ("Chinese (Mandarin)_Soft_Girl", "柔和少女", "温柔 · 女声"),
    ("Chinese (Mandarin)_Cute_Spirit", "憨憨萌兽", "憨 · 女声"),
    ("female-shaonv-jingpin", "少女音色", "beta · 女声"),
    ("Chinese (Mandarin)_Crisp_Girl", "清脆少女", "清脆 · 女声"),
    ("Chinese (Mandarin)_Warm_Girl", "温暖少女", "温暖 · 女声"),
    ("female-tianmei-jingpin", "甜美女性", "beta · 女声"),
]


@app.get("/v1/voices")
def voices():
    return {
        "engine": "minimax",
        "license": "MiniMax 开放平台 · 付费 API 商用授权（以你签署的条款为准）",
        "voices": [
            {"id": vid, "name": name, "note": note + " · 商用授权", "commercial": True}
            for vid, name, note in SOFT_VOICES
        ],
    }


@app.post("/v1/audio/speech")
def speech(req: SpeechReq, http: Request):
    # Key 两个来源：网关自己的环境变量（推荐，不进业务库），或页面表单里填的
    # API Key（backend 按 OpenAI 习惯放在 Authorization: Bearer 里带过来）。
    # 后者让「页面填 Key → 试听 → 保存」一条链闭合，不必再去改启动脚本重启。
    key = os.getenv("MINIMAX_API_KEY", "").strip()
    if not key:
        auth = http.headers.get("authorization", "")
        if auth.lower().startswith("bearer "):
            key = auth[7:].strip()
    if not key:
        raise HTTPException(500, "没配 Key：在页面「API Key」填，或给网关设 MINIMAX_API_KEY 环境变量")

    text = (req.input or "").strip()
    if not text:
        raise HTTPException(400, "input 不能为空")

    payload = {
        "model": os.getenv("MINIMAX_TTS_MODEL", "speech-02-hd"),
        "text": text,
        "stream": False,
        "voice_setting": {
            "voice_id": (req.voice or DEFAULT_VOICE).strip(),
            # 软萌的第二个旋钮：语速略放慢会更"黏"，但不低于 0.85 否则像慢放。
            "speed": req.speed if req.speed > 0 else 1.0,
            "vol": 1.0,
            "pitch": 0,
        },
        "audio_setting": {
            "sample_rate": 32000,
            "bitrate": 128000,
            "format": "mp3",
        },
    }

    j = _post(os.getenv("MINIMAX_API_BASE", DEFAULT_API), payload, key)

    base = j.get("base_resp") or {}
    code = base.get("status_code")
    if code not in (0, None):
        raise HTTPException(502, f"MiniMax 拒绝: {base.get('status_msg')}")

    hex_audio = (j.get("data") or {}).get("audio") or ""
    if not hex_audio:
        raise HTTPException(502, "MiniMax 返回空音频（检查音色 ID 是否存在）")

    # MiniMax 的 audio 字段是 hex 字符串，不是 base64。
    return Response(content=bytes.fromhex(hex_audio), media_type="audio/mpeg")


if __name__ == "__main__":
    import uvicorn

    ap = argparse.ArgumentParser()
    ap.add_argument("--host", default="127.0.0.1")
    ap.add_argument("--port", type=int, default=9883)
    a = ap.parse_args()
    uvicorn.run(app, host=a.host, port=a.port, log_level="info")
