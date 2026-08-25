from __future__ import annotations

import io
import json
import sys
import tempfile
import unittest
import urllib.error
from pathlib import Path
from unittest import mock


LAB_ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(LAB_ROOT / "lib"))

import qwen_chat  # noqa: E402


def fake_response(content: str, completion_tokens: int = 7):
    payload = {
        "choices": [{"message": {"role": "assistant", "content": content}}],
        "usage": {"completion_tokens": completion_tokens},
    }
    response = mock.MagicMock()
    response.read.return_value = json.dumps(payload).encode("utf-8")
    response.__enter__.return_value = io.BytesIO(json.dumps(payload).encode("utf-8"))
    response.__exit__.return_value = False
    return response


class RequestShapeTests(unittest.TestCase):
    """The server, not the client, decides what "normal" is."""

    def send(self, **kwargs) -> dict:
        captured = {}

        def urlopen(request, timeout=None):
            captured["url"] = request.full_url
            captured["headers"] = dict(request.headers)
            captured["body"] = json.loads(request.data.decode("utf-8"))
            return fake_response("ok")

        with mock.patch.object(qwen_chat.urllib.request, "urlopen", urlopen):
            qwen_chat.ask("http://127.0.0.1:8088/v1", "secret", "hi", **kwargs)
        return captured

    def defaults(self) -> dict:
        return {"think": False, "max_tokens": 64, "temperature": 0.0, "timeout": 5}

    def test_omits_thinking_kwargs_unless_asked(self) -> None:
        # Thinking is off in the unit file because it costs 4.4x time-to-answer
        # here. A client that always sent the flag would silently take that
        # decision away from the service.
        captured = self.send(**self.defaults())
        self.assertNotIn("chat_template_kwargs", captured["body"])

    def test_sends_thinking_kwargs_when_asked(self) -> None:
        captured = self.send(**{**self.defaults(), "think": True})
        self.assertEqual(
            {"enable_thinking": True}, captured["body"]["chat_template_kwargs"]
        )

    def test_authorises_with_bearer_token(self) -> None:
        captured = self.send(**self.defaults())
        self.assertEqual("Bearer secret", captured["headers"]["Authorization"])
        self.assertEqual(
            "http://127.0.0.1:8088/v1/chat/completions", captured["url"]
        )


class KeyHandlingTests(unittest.TestCase):
    def test_reads_and_strips_the_key_file(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "api-key"
            path.write_text("abc123\n", encoding="utf-8")
            self.assertEqual("abc123", qwen_chat.read_key(path))

    def test_missing_key_file_explains_itself(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            with self.assertRaises(SystemExit) as raised:
                qwen_chat.read_key(Path(temporary) / "absent")
        self.assertIn("no API key", str(raised.exception))


class FailureMessageTests(unittest.TestCase):
    """A rejected key and an absent server are different problems."""

    def ask_raising(self, error):
        with mock.patch.object(
            qwen_chat.urllib.request, "urlopen", side_effect=error
        ):
            with self.assertRaises(SystemExit) as raised:
                qwen_chat.ask(
                    "http://127.0.0.1:8088/v1", "k", "hi",
                    think=False, max_tokens=8, temperature=0.0, timeout=1,
                )
        return str(raised.exception)

    def test_unauthorised_names_the_key(self) -> None:
        error = urllib.error.HTTPError(
            "u", 401, "Unauthorized", {}, io.BytesIO(b"{}")
        )
        self.assertIn("API key", self.ask_raising(error))

    def test_unreachable_names_the_service(self) -> None:
        message = self.ask_raising(urllib.error.URLError("connection refused"))
        self.assertIn("qwen-lab-server", message)


class CommandLineTests(unittest.TestCase):
    def test_joins_prompt_words_and_prints_only_the_answer(self) -> None:
        with mock.patch.object(qwen_chat, "ask", return_value=("Paris", {})) as ask:
            with mock.patch("sys.stdout", new_callable=io.StringIO) as out:
                exit_code = qwen_chat.main(
                    ["--api-key", "k", "--quiet", "capital", "of", "France?"]
                )
        self.assertEqual(0, exit_code)
        self.assertEqual("Paris", out.getvalue().strip())
        self.assertEqual("capital of France?", ask.call_args.args[2])

    def test_empty_prompt_is_refused(self) -> None:
        with mock.patch("sys.stderr", new_callable=io.StringIO):
            with self.assertRaises(SystemExit):
                qwen_chat.main(["--api-key", "k"])


class EmptyAnswerTests(unittest.TestCase):
    """Thinking can eat the whole budget; a blank line would look like a bug."""

    def ask_returning(self, choice: dict) -> str:
        payload = {"choices": [choice], "usage": {"completion_tokens": 200}}
        raw = json.dumps(payload).encode("utf-8")

        def urlopen(request, timeout=None):
            response = mock.MagicMock()
            response.__enter__.return_value = io.BytesIO(raw)
            response.__exit__.return_value = False
            return response

        with mock.patch.object(qwen_chat.urllib.request, "urlopen", urlopen):
            answer, _ = qwen_chat.ask(
                "http://127.0.0.1:8088/v1", "k", "hi",
                think=True, max_tokens=200, temperature=0.0, timeout=5,
            )
        return answer

    def test_truncated_while_reasoning_explains_and_suggests_a_fix(self) -> None:
        answer = self.ask_returning({
            "finish_reason": "length",
            "message": {"content": "", "reasoning_content": "Let me count..."},
        })
        self.assertIn("token limit", answer)
        self.assertIn("still reasoning", answer)
        self.assertIn("--max-tokens", answer)

    def test_reasoning_only_is_reported_as_no_answer(self) -> None:
        answer = self.ask_returning({
            "finish_reason": "stop",
            "message": {"content": "", "reasoning_content": "hmm"},
        })
        self.assertIn("only reasoning", answer)

    def test_a_real_answer_is_returned_untouched(self) -> None:
        self.assertEqual("Paris", self.ask_returning({
            "finish_reason": "stop", "message": {"content": "  Paris  "},
        }))


if __name__ == "__main__":
    unittest.main()


class UnixSocketTests(unittest.TestCase):
    """The contained model has no network; the socket is the only way in."""

    def test_a_missing_socket_says_how_to_start_it(self) -> None:
        with self.assertRaises(SystemExit) as raised:
            qwen_chat.request_over_socket(
                "/nonexistent/api.sock", "/v1/models", None, "", 5)
        message = str(raised.exception)
        self.assertIn("no socket", message)
        self.assertIn("systemctl start qwen-sandbox", message)

    def test_permission_denied_explains_the_write_bit(self) -> None:
        # Connecting to a Unix socket needs *write* permission on it, not read.
        # That is the unintuitive part, so the message says it rather than
        # leaving somebody to check that the file is readable and be baffled.
        with mock.patch.object(qwen_chat.UnixSocketConnection, "connect",
                               side_effect=PermissionError):
            with self.assertRaises(SystemExit) as raised:
                qwen_chat.request_over_socket(
                    "/run/qwen-sandbox/api.sock", "/v1/models", None, "", 5)
        message = str(raised.exception)
        self.assertIn("write permission", message)
        self.assertIn("qwensandbox", message)

    def test_the_socket_path_bypasses_the_key_file(self) -> None:
        # A machine running only the sandboxed model has no /etc/qwen-lab, so
        # insisting on a key there would make the client unusable.
        with mock.patch.object(qwen_chat, "ask", return_value=("ok", {})) as ask:
            with mock.patch.object(qwen_chat, "read_key") as read_key:
                with mock.patch("sys.stdout", new_callable=io.StringIO):
                    qwen_chat.main(["--socket", "/run/qwen-sandbox/api.sock",
                                    "--quiet", "hello"])
        read_key.assert_not_called()
        self.assertEqual("/run/qwen-sandbox/api.sock",
                         ask.call_args.kwargs["unix_socket"])


class StillLoadingTests(unittest.TestCase):
    """`systemctl start` returns long before the model is answerable."""

    def test_a_503_says_to_wait_rather_than_quoting_an_error(self) -> None:
        message = qwen_chat.loading_message(
            b'{"error":{"message":"Loading model","code":503}}')
        self.assertIn("still loading", message)
        self.assertIn("Try again shortly", message)
        # The raw phrasing is not echoed back — it reads as a fault.
        self.assertNotIn("unavailable_error", message)

    def test_an_unusual_503_keeps_its_detail(self) -> None:
        message = qwen_chat.loading_message(
            b'{"error":{"message":"no slot available"}}')
        self.assertIn("no slot available", message)

    def test_a_body_that_is_not_json_still_produces_advice(self) -> None:
        self.assertIn("still loading", qwen_chat.loading_message(b"<html>502</html>"))
