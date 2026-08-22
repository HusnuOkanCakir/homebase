"""Talk to the lab's local model server.

A client rather than an application. Open WebUI would be the obvious choice and
would make a natural Homebase manifest, but that is the Stage 2 integration this
project has deliberately parked — so this stays a script in the lab, speaking
the same OpenAI-compatible API any other client would.

Standard library only, like everything else here. It reads the API key from the
file systemd/install wrote, so nothing prints a credential and nothing has to be
pasted into a shell history.

    ./bin/qwen-chat "explain what a home server is for"
    ./bin/qwen-chat --think "why is the sky blue?"
    echo "summarise this" | ./bin/qwen-chat --stdin

The server has thinking off by default, because on this hardware it costs four
times the tokens to answer a simple question. --think turns it back on for a
request that deserves it.
"""

from __future__ import annotations

import argparse
import json
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path

DEFAULT_ENDPOINT = "http://127.0.0.1:8088/v1"
DEFAULT_KEY_FILE = Path("/etc/qwen-lab/api-key")


def read_key(path: Path) -> str:
    try:
        return path.read_text(encoding="utf-8").strip()
    except PermissionError:
        sys.exit(f"cannot read {path} — run with sudo, or pass --api-key")
    except FileNotFoundError:
        sys.exit(f"no API key at {path} — is the service installed?")


def ask(endpoint: str, key: str, prompt: str, *, think: bool,
        max_tokens: int, temperature: float, timeout: int) -> tuple[str, dict]:
    body = {
        "messages": [{"role": "user", "content": prompt}],
        "max_tokens": max_tokens,
        "temperature": temperature,
    }
    # Only sent when overriding the server's default, so the server stays the
    # single place that decides what "normal" is.
    if think:
        body["chat_template_kwargs"] = {"enable_thinking": True}

    request = urllib.request.Request(
        f"{endpoint}/chat/completions",
        data=json.dumps(body).encode("utf-8"),
        headers={"Content-Type": "application/json",
                 "Authorization": f"Bearer {key}"},
    )
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            payload = json.load(response)
    except urllib.error.HTTPError as error:
        if error.code == 401:
            sys.exit("the server rejected the API key")
        sys.exit(f"the server answered {error.code}: {error.read()[:200].decode(errors='replace')}")
    except urllib.error.URLError as error:
        sys.exit(f"cannot reach {endpoint} — is qwen-lab-server running? ({error.reason})")

    choice = payload["choices"][0]
    message = choice.get("message", {})
    content = (message.get("content") or "").strip()

    # Thinking is expensive here: a reasoning model can spend the whole token
    # budget before it writes a word of answer, and then `content` comes back
    # empty. Printing a blank line would look like the server misbehaved, so
    # say what actually happened.
    if not content:
        reasoning = (message.get("reasoning_content") or "").strip()
        if choice.get("finish_reason") == "length":
            hint = "the token limit was reached before an answer was written"
            if reasoning:
                hint += " — it was still reasoning"
            content = f"[no answer: {hint}. Try a larger --max-tokens.]"
        elif reasoning:
            content = "[no answer: the model produced only reasoning.]"
        else:
            content = "[no answer: the model returned empty content.]"

    return content, payload.get("usage", {})


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__,
                                     formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("prompt", nargs="*", help="the question")
    parser.add_argument("--stdin", action="store_true", help="read the prompt from stdin")
    parser.add_argument("--think", action="store_true",
                        help="let the model reason first; slower, better on hard questions")
    parser.add_argument("--endpoint", default=DEFAULT_ENDPOINT)
    parser.add_argument("--api-key")
    parser.add_argument("--api-key-file", type=Path, default=DEFAULT_KEY_FILE)
    parser.add_argument("--max-tokens", type=int, default=512)
    parser.add_argument("--temperature", type=float, default=0.7)
    parser.add_argument("--timeout", type=int, default=600)
    parser.add_argument("--quiet", action="store_true", help="answer only, no timing line")
    args = parser.parse_args(argv)

    prompt = sys.stdin.read().strip() if args.stdin else " ".join(args.prompt).strip()
    if not prompt:
        parser.error("nothing to ask — give a prompt or --stdin")

    key = args.api_key or read_key(args.api_key_file)

    started = time.monotonic()
    answer, usage = ask(args.endpoint, key, prompt, think=args.think,
                        max_tokens=args.max_tokens, temperature=args.temperature,
                        timeout=args.timeout)
    elapsed = time.monotonic() - started

    print(answer)
    if not args.quiet:
        generated = usage.get("completion_tokens")
        # Printed because on this hardware the rate is the thing worth knowing,
        # and it changes with what else the server is doing.
        rate = f", {generated / elapsed:.1f} tok/s" if generated and elapsed > 0 else ""
        print(f"\n[{elapsed:.1f}s, {generated} tokens{rate}]", file=sys.stderr)
    return 0

