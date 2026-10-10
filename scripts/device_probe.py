#!/usr/bin/env python3
"""手机端的参考实现：连中继、握手、配对、拉一次会话列表。

存在的意义有两个：一是自建中继的人可以拿它验证「我这套到底通没通」，二是 iOS
端的握手与消息格式有一份可以直接跑起来对照的实现——照着一段能跑的代码写，比
照着协议文档写少踩很多坑。

用法：

    python3 device_probe.py --relay ws://127.0.0.1:8799 --key <开发机公钥十六进制> --code <配对码>

开发机标识、公钥和配对码都来自开发机上配对命令的 JSON 输出。
"""

from __future__ import annotations

import argparse
import asyncio
import base64
import importlib
import json
import os
import sys

_CLI_SRC = os.path.join(os.path.dirname(__file__), "..", "..", "cli", "src")
sys.path.insert(0, _CLI_SRC)


def _load_peer_protocol():
    """加载同仓配套命令行里的协议与加密实现，避免探针再维护一份。"""
    for name in os.listdir(_CLI_SRC):
        remote = os.path.join(_CLI_SRC, name, "remote")
        if os.path.isfile(os.path.join(remote, "protocol.py")):
            protocol_mod = importlib.import_module(f"{name}.remote.protocol")
            crypto_mod = importlib.import_module(f"{name}.remote.crypto")
            return protocol_mod, crypto_mod.Handshake, crypto_mod.generate_private_key_bytes
    raise ImportError("找不到开发机侧的协议实现")


protocol, Handshake, generate_private_key_bytes = _load_peer_protocol()

_IDENTITY_FILE = os.path.expanduser("~/.cache/corral/remote/device-probe.key")


def _identity() -> bytes:
    """探针自己的长期密钥。配对是绑公钥的，换一把就要重新配对，所以存下来。"""
    try:
        data = open(_IDENTITY_FILE, "rb").read()
        if len(data) == 32:
            return data
    except OSError:
        pass
    key = generate_private_key_bytes()
    os.makedirs(os.path.dirname(_IDENTITY_FILE), exist_ok=True)
    with open(_IDENTITY_FILE, "wb") as handle:
        handle.write(key)
    os.chmod(_IDENTITY_FILE, 0o600)
    return key


def _decode_key(value: str) -> bytes:
    try:
        return bytes.fromhex(value)
    except ValueError:
        padded = value + "=" * (-len(value) % 4)
        return base64.urlsafe_b64decode(padded)


async def run(args) -> int:
    import websockets

    handshake = Handshake(_identity())
    host_key = _decode_key(args.key)
    if args.direct:
        url = args.direct.rstrip("/")
    else:
        host = args.host or __import__(
            "corral.remote.crypto", fromlist=["routing_id_from_x25519"]
        ).routing_id_from_x25519(host_key)
        url = f"{args.relay.rstrip('/')}/v2/device?host={host}"
    print(f"连接 {url}")

    async with websockets.connect(
        url, max_size=8 * 1024 * 1024, subprotocols=[protocol.SUBPROTOCOL]
    ) as socket:
        channel_id = b"\x00" * protocol.CHANNEL_ID_LEN

        # 中继先告诉我们分配到的通道标识
        kind, channel_id, _ = protocol.decode_frame(await socket.recv())
        assert kind == protocol.FRAME_DEVICE_OPEN, f"期待通道分配，收到 {kind:#x}"
        print(f"通道已建立：{channel_id.hex()}")

        await socket.send(
            protocol.encode_frame(
                protocol.FRAME_HELLO, channel_id, handshake.static_public + handshake.ephemeral_public
            )
        )
        kind, _cid, payload = protocol.decode_frame(await socket.recv())
        assert kind == protocol.FRAME_HELLO, f"期待握手回应，收到 {kind:#x}"
        secure = handshake.complete(host_key, payload)
        print("端到端加密通道已就绪")

        pending: dict[int, asyncio.Future] = {}
        next_id = [0]

        async def call(method: str, params: dict | None = None):
            next_id[0] += 1
            request_id = next_id[0]
            future = asyncio.get_running_loop().create_future()
            pending[request_id] = future
            frame = secure.encrypt(protocol.dumps(protocol.request(request_id, method, params)))
            await socket.send(protocol.encode_frame(protocol.FRAME_DATA, channel_id, frame))
            message = await asyncio.wait_for(future, timeout=20)
            if message.get("e"):
                # 出错就当场喊出来，别让调用方拿一个空 dict 继续往下跑——
                # 之前就因为静默吞掉「没配对」而误以为链路是通的。
                raise RuntimeError(f"{method} 失败：{message['e'].get('message')}")
            return message

        async def reader():
            async for raw in socket:
                kind, _cid, payload = protocol.decode_frame(raw)
                if kind != protocol.FRAME_DATA:
                    continue
                message = protocol.loads(secure.decrypt(payload))
                if message.get("t") == "res":
                    future = pending.pop(int(message.get("id") or 0), None)
                    if future and not future.done():
                        future.set_result(message)
                elif message.get("t") == "evt":
                    print(f"  ← 推送 {message.get('c')}")

        task = asyncio.create_task(reader())
        try:
            if args.code:
                result = await call("pair", {"code": args.code, "name": args.name, "platform": "probe"})
                print("配对成功：", json.dumps(result.get("d"), ensure_ascii=False))
            hello = await call("hello", {"name": args.name})
            print("开发机：", json.dumps(hello.get("d"), ensure_ascii=False)[:400])
            listing = await call("sessions.list", {"limit": args.limit})
            sessions = (listing.get("d") or {}).get("sessions") or []
            print(f"会话 {len(sessions)} 条：")
            for item in sessions:
                print(
                    f"  [{item['runtime']:<8}] {item['title'][:28]:<30} "
                    f"{'运行中' if item['live'] else '已结束'}  {item['cwd_display']}"
                )
            if sessions and args.messages:
                detail = await call("session.messages", {"key": sessions[0]["key"], "limit": 5})
                for message in (detail.get("d") or {}).get("messages") or []:
                    tools = message.get("tools") or []
                    print(f"  · {message['role']}: {(message.get('text') or '')[:60]!r} 工具 {len(tools)}")
                    for tool in tools[:3]:
                        print(f"      - {tool['kind']}: {tool['summary'][:60]}")
        finally:
            task.cancel()
    return 0


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--relay", default="", help="中继地址，例如 ws://127.0.0.1:8799")
    parser.add_argument("--direct", default="", help="局域网直连地址，例如 ws://192.168.1.5:8737；给了就不走中继")
    parser.add_argument("--host", default="", help="开发机路由标识；省略时从公钥派生")
    parser.add_argument("--key", required=True, help="开发机长期公钥（十六进制或 base64url）")
    parser.add_argument("--code", default="", help="配对码；已配对过就不用给")
    parser.add_argument("--name", default="探针")
    parser.add_argument("--limit", type=int, default=5)
    parser.add_argument("--messages", action="store_true", help="顺带拉一条会话的消息")
    args = parser.parse_args()
    if not args.direct and not args.relay:
        parser.error("要么给 --direct，要么给 --relay")
    return asyncio.run(run(args))


if __name__ == "__main__":
    raise SystemExit(main())
