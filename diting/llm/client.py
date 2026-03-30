"""Async LLM client wrapping OpenAI v1 compatible chat completions API."""

import json

import httpx

from diting.log import get_logger

logger = get_logger("llm.client")


class LLMError(Exception):
    """Error from the LLM client."""


class LLMClient:
    """Async wrapper around an OpenAI v1 compatible chat completions endpoint.

    Uses ``httpx.AsyncClient`` for HTTP requests. The caller is responsible
    for calling :meth:`close` when the client is no longer needed.
    """

    _MAX_ATTEMPTS = 2

    def __init__(
        self,
        base_url: str,
        api_key: str,
        model: str,
        timeout: int = 60,
    ) -> None:
        self._base_url = base_url.rstrip("/")
        self._model = model
        self._http = httpx.AsyncClient(
            headers={
                "Authorization": f"Bearer {api_key}",
                "Content-Type": "application/json",
            },
            timeout=httpx.Timeout(timeout),
        )

    # ------------------------------------------------------------------
    # Public API
    # ------------------------------------------------------------------

    async def chat(
        self,
        system_prompt: str,
        user_message: str,
        json_mode: bool = False,
    ) -> str:
        """Send a chat completion request.

        Args:
            system_prompt: System message content.
            user_message: User message content.
            json_mode: If True, request JSON response format.

        Returns:
            The assistant's response content as a string.

        Raises:
            LLMError: On API errors, timeouts, or empty responses.
        """
        body: dict = {
            "model": self._model,
            "messages": [
                {"role": "system", "content": system_prompt},
                {"role": "user", "content": user_message},
            ],
        }
        if json_mode:
            body["response_format"] = {"type": "json_object"}

        url = f"{self._base_url}/chat/completions"
        logger.debug(
            "LLM request: model=%s json_mode=%s prompt_len=%d msg_len=%d",
            self._model, json_mode, len(system_prompt), len(user_message),
        )

        last_error: LLMError | None = None
        for attempt in range(self._MAX_ATTEMPTS):
            try:
                response = await self._http.post(url, json=body)
            except httpx.TimeoutException as exc:
                last_error = LLMError(f"Request timed out: {exc}")
                logger.warning("LLM request timeout (attempt %d): %s", attempt + 1, exc)
                continue
            except httpx.RequestError as exc:
                last_error = LLMError(f"Request failed: {exc}")
                logger.warning("LLM request error (attempt %d): %s", attempt + 1, exc)
                continue

            if response.status_code >= 500:
                last_error = LLMError(
                    f"HTTP {response.status_code}: {response.text}"
                )
                logger.warning("LLM server error (attempt %d): HTTP %d", attempt + 1, response.status_code)
                continue

            if response.status_code >= 400:
                logger.error("LLM client error: HTTP %d — %s", response.status_code, response.text[:200])
                raise LLMError(
                    f"HTTP {response.status_code}: {response.text}"
                )

            # Parse successful response.
            try:
                data = response.json()
                content = data["choices"][0]["message"]["content"]
            except (KeyError, IndexError, TypeError, ValueError) as exc:
                raise LLMError(
                    f"Malformed LLM response: {exc}"
                ) from exc
            if not content:
                raise LLMError("Empty response content from LLM")

            usage = data.get("usage", {})
            logger.info(
                "LLM response OK: tokens=%s, response_len=%d",
                usage if usage else "N/A", len(content),
            )
            logger.debug("LLM response content: %.500s", content)
            return content

        # All retry attempts exhausted.
        raise last_error  # type: ignore[misc]

    async def chat_json(
        self,
        system_prompt: str,
        user_message: str,
    ) -> dict:
        """Send a chat completion request and parse the JSON response.

        Returns:
            Parsed JSON dict.

        Raises:
            LLMError: On API errors, timeouts, empty responses, or invalid JSON.
        """
        raw = await self.chat(system_prompt, user_message, json_mode=True)
        try:
            parsed = json.loads(raw)
        except (json.JSONDecodeError, TypeError) as exc:
            raise LLMError(f"Failed to parse JSON response: {exc}") from exc
        if not isinstance(parsed, dict):
            raise LLMError(
                f"Expected JSON object, got {type(parsed).__name__}"
            )
        return parsed

    async def close(self) -> None:
        """Close the underlying HTTP client."""
        await self._http.aclose()
