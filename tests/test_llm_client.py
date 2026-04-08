"""Tests for diting.llm.client — LLMClient async wrapper."""

import json
from unittest.mock import AsyncMock, patch

import httpx
import pytest

from diting.llm.client import LLMClient, LLMError


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

BASE_URL = "https://api.example.com/v1"
API_KEY = "sk-test-key"
MODEL = "gpt-4o-mini"


def _make_response(content: str, status_code: int = 200) -> httpx.Response:
    """Build a mock httpx.Response with an OpenAI-style JSON body."""
    body = {
        "choices": [
            {
                "index": 0,
                "message": {"role": "assistant", "content": content},
                "finish_reason": "stop",
            }
        ]
    }
    return httpx.Response(
        status_code=status_code,
        json=body,
        request=httpx.Request("POST", f"{BASE_URL}/chat/completions"),
    )


def _make_error_response(status_code: int, body: str = "error") -> httpx.Response:
    """Build a mock httpx.Response representing an API error."""
    return httpx.Response(
        status_code=status_code,
        text=body,
        request=httpx.Request("POST", f"{BASE_URL}/chat/completions"),
    )


# ---------------------------------------------------------------------------
# Tests
# ---------------------------------------------------------------------------


class TestChatSuccess:
    """Successful chat completion returns assistant content."""

    async def test_chat_success(self):
        client = LLMClient(base_url=BASE_URL, api_key=API_KEY, model=MODEL)
        mock_response = _make_response("Hello, world!")

        with patch.object(client._http, "post", new_callable=AsyncMock, return_value=mock_response):
            result = await client.chat("You are helpful.", "Say hello.")

        assert result == "Hello, world!"
        await client.close()


class TestChatJsonSuccess:
    """chat_json returns a parsed dict from a valid JSON response."""

    async def test_chat_json_success(self):
        client = LLMClient(base_url=BASE_URL, api_key=API_KEY, model=MODEL)
        payload = {"answer": "42", "confidence": 0.99}
        mock_response = _make_response(json.dumps(payload))

        with patch.object(client._http, "post", new_callable=AsyncMock, return_value=mock_response):
            result = await client.chat_json("Return JSON.", "What is the answer?")

        assert result == payload
        await client.close()


class TestChatJsonInvalidJson:
    """chat_json raises LLMError when the response is not valid JSON."""

    async def test_chat_json_invalid_json(self):
        client = LLMClient(base_url=BASE_URL, api_key=API_KEY, model=MODEL)
        mock_response = _make_response("this is not json")

        with patch.object(client._http, "post", new_callable=AsyncMock, return_value=mock_response):
            with pytest.raises(LLMError, match="(?i)json"):
                await client.chat_json("Return JSON.", "Give me data.")

        await client.close()


class TestChatHttpError:
    """Non-2xx responses raise LLMError with status code information."""

    async def test_chat_http_error(self):
        client = LLMClient(base_url=BASE_URL, api_key=API_KEY, model=MODEL)
        mock_response = _make_error_response(400, "Bad Request: invalid model")

        with patch.object(client._http, "post", new_callable=AsyncMock, return_value=mock_response):
            with pytest.raises(LLMError, match="400"):
                await client.chat("System.", "User.")

        await client.close()


class TestChatTimeout:
    """Timeout during request raises LLMError with timeout information."""

    async def test_chat_timeout(self):
        client = LLMClient(base_url=BASE_URL, api_key=API_KEY, model=MODEL)

        with patch.object(
            client._http,
            "post",
            new_callable=AsyncMock,
            side_effect=httpx.TimeoutException("Connection timed out"),
        ):
            with pytest.raises(LLMError, match="(?i)timed out"):
                await client.chat("System.", "User.")

        await client.close()


class TestChatEmptyContent:
    """Empty or None content in the response raises LLMError."""

    async def test_chat_empty_content(self):
        client = LLMClient(base_url=BASE_URL, api_key=API_KEY, model=MODEL)
        mock_response = _make_response("")

        with patch.object(client._http, "post", new_callable=AsyncMock, return_value=mock_response):
            with pytest.raises(LLMError, match="(?i)empty"):
                await client.chat("System.", "User.")

        await client.close()

    async def test_chat_none_content(self):
        client = LLMClient(base_url=BASE_URL, api_key=API_KEY, model=MODEL)
        body = {"choices": [{"index": 0, "message": {"role": "assistant", "content": None}}]}
        mock_response = httpx.Response(
            status_code=200,
            json=body,
            request=httpx.Request("POST", f"{BASE_URL}/chat/completions"),
        )

        with patch.object(client._http, "post", new_callable=AsyncMock, return_value=mock_response):
            with pytest.raises(LLMError, match="(?i)empty"):
                await client.chat("System.", "User.")

        await client.close()


class TestRetryOn5xx:
    """5xx errors and timeouts are retried once (max 2 attempts total)."""

    async def test_chat_retry_on_5xx(self):
        client = LLMClient(base_url=BASE_URL, api_key=API_KEY, model=MODEL)
        error_response = _make_error_response(500, "Internal Server Error")
        success_response = _make_response("Recovered!")

        mock_post = AsyncMock(side_effect=[error_response, success_response])

        with patch.object(client._http, "post", mock_post):
            result = await client.chat("System.", "User.")

        assert result == "Recovered!"
        assert mock_post.call_count == 2
        await client.close()

    async def test_chat_retry_on_timeout_then_success(self):
        client = LLMClient(base_url=BASE_URL, api_key=API_KEY, model=MODEL)
        success_response = _make_response("Recovered after timeout!")

        mock_post = AsyncMock(
            side_effect=[httpx.TimeoutException("timed out"), success_response]
        )

        with patch.object(client._http, "post", mock_post):
            result = await client.chat("System.", "User.")

        assert result == "Recovered after timeout!"
        assert mock_post.call_count == 2
        await client.close()

    async def test_chat_retry_exhausted_5xx(self):
        """Two consecutive 5xx errors exhaust retries and raise LLMError."""
        client = LLMClient(base_url=BASE_URL, api_key=API_KEY, model=MODEL)
        error_response = _make_error_response(502, "Bad Gateway")

        mock_post = AsyncMock(return_value=error_response)

        with patch.object(client._http, "post", mock_post):
            with pytest.raises(LLMError, match="502"):
                await client.chat("System.", "User.")

        assert mock_post.call_count == 2
        await client.close()

    async def test_chat_no_retry_on_4xx(self):
        """4xx errors are NOT retried."""
        client = LLMClient(base_url=BASE_URL, api_key=API_KEY, model=MODEL)
        error_response = _make_error_response(422, "Unprocessable Entity")

        mock_post = AsyncMock(return_value=error_response)

        with patch.object(client._http, "post", mock_post):
            with pytest.raises(LLMError, match="422"):
                await client.chat("System.", "User.")

        assert mock_post.call_count == 1
        await client.close()


class TestJsonModeRequestFormat:
    """When json_mode=True, the request body must include response_format."""

    async def test_chat_json_mode_request_format(self):
        client = LLMClient(base_url=BASE_URL, api_key=API_KEY, model=MODEL)
        mock_response = _make_response('{"key": "value"}')
        mock_post = AsyncMock(return_value=mock_response)

        with patch.object(client._http, "post", mock_post):
            await client.chat("System.", "User.", json_mode=True)

        call_kwargs = mock_post.call_args
        request_body = call_kwargs.kwargs.get("json") or call_kwargs[1].get("json")
        assert request_body["response_format"] == {"type": "json_object"}
        await client.close()

    async def test_chat_no_json_mode_omits_response_format(self):
        client = LLMClient(base_url=BASE_URL, api_key=API_KEY, model=MODEL)
        mock_response = _make_response("plain text")
        mock_post = AsyncMock(return_value=mock_response)

        with patch.object(client._http, "post", mock_post):
            await client.chat("System.", "User.", json_mode=False)

        call_kwargs = mock_post.call_args
        request_body = call_kwargs.kwargs.get("json") or call_kwargs[1].get("json")
        assert "response_format" not in request_body
        await client.close()

class TestRequestError:
    """Non-timeout transport errors are wrapped in LLMError."""

    async def test_chat_connect_error(self):
        client = LLMClient(base_url=BASE_URL, api_key=API_KEY, model=MODEL)

        with patch.object(
            client._http,
            "post",
            new_callable=AsyncMock,
            side_effect=httpx.ConnectError("Connection refused"),
        ):
            with pytest.raises(LLMError, match="(?i)request failed"):
                await client.chat("System.", "User.")

        await client.close()

    async def test_chat_retry_on_request_error_then_success(self):
        client = LLMClient(base_url=BASE_URL, api_key=API_KEY, model=MODEL)
        success_response = _make_response("Recovered!")

        mock_post = AsyncMock(
            side_effect=[httpx.ConnectError("refused"), success_response]
        )

        with patch.object(client._http, "post", mock_post):
            result = await client.chat("System.", "User.")

        assert result == "Recovered!"
        assert mock_post.call_count == 2
        await client.close()


class TestMalformedResponse:
    """Malformed 2xx responses raise LLMError instead of raw exceptions."""

    async def test_chat_missing_choices_key(self):
        client = LLMClient(base_url=BASE_URL, api_key=API_KEY, model=MODEL)
        mock_response = httpx.Response(
            status_code=200,
            json={"id": "chatcmpl-123"},
            request=httpx.Request("POST", f"{BASE_URL}/chat/completions"),
        )

        with patch.object(client._http, "post", new_callable=AsyncMock, return_value=mock_response):
            with pytest.raises(LLMError, match="(?i)malformed"):
                await client.chat("System.", "User.")

        await client.close()

    async def test_chat_empty_choices_list(self):
        client = LLMClient(base_url=BASE_URL, api_key=API_KEY, model=MODEL)
        mock_response = httpx.Response(
            status_code=200,
            json={"choices": []},
            request=httpx.Request("POST", f"{BASE_URL}/chat/completions"),
        )

        with patch.object(client._http, "post", new_callable=AsyncMock, return_value=mock_response):
            with pytest.raises(LLMError, match="(?i)malformed"):
                await client.chat("System.", "User.")

        await client.close()

    async def test_chat_non_json_200_body(self):
        client = LLMClient(base_url=BASE_URL, api_key=API_KEY, model=MODEL)
        mock_response = httpx.Response(
            status_code=200,
            text="<html>Gateway Error</html>",
            request=httpx.Request("POST", f"{BASE_URL}/chat/completions"),
        )

        with patch.object(client._http, "post", new_callable=AsyncMock, return_value=mock_response):
            with pytest.raises(LLMError, match="(?i)malformed"):
                await client.chat("System.", "User.")

        await client.close()


class TestChatJsonNonDictResponse:
    """chat_json raises LLMError when valid JSON is not a dict."""

    async def test_chat_json_returns_list(self):
        client = LLMClient(base_url=BASE_URL, api_key=API_KEY, model=MODEL)
        mock_response = _make_response('[1, 2, 3]')

        with patch.object(client._http, "post", new_callable=AsyncMock, return_value=mock_response):
            with pytest.raises(LLMError, match="(?i)expected json object"):
                await client.chat_json("Return JSON.", "Give me data.")

        await client.close()

    async def test_chat_json_returns_string(self):
        client = LLMClient(base_url=BASE_URL, api_key=API_KEY, model=MODEL)
        mock_response = _make_response('"just a string"')

        with patch.object(client._http, "post", new_callable=AsyncMock, return_value=mock_response):
            with pytest.raises(LLMError, match="(?i)expected json object"):
                await client.chat_json("Return JSON.", "Give me data.")

        await client.close()


class TestClose:
    """close() delegates to the underlying httpx client's aclose()."""

    async def test_close(self):
        client = LLMClient(base_url=BASE_URL, api_key=API_KEY, model=MODEL)

        with patch.object(client._http, "aclose", new_callable=AsyncMock) as mock_aclose:
            await client.close()

        mock_aclose.assert_awaited_once()
