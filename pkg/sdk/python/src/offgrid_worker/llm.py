"""Внешняя языковая модель для человекочитаемых объяснений (XAI) — общий интерфейс воркеров.

- LanguageModel — интерфейс: системная инструкция + факты расчёта → текст;
- JoinModel — заглушка без сети: склеивает факты (тесты, офлайн, режим по умолчанию);
- OpenAICompatibleModel — любой OpenAI-совместимый /chat/completions (GigaChat через
  совместимый шлюз, ProxyAPI, локальная модель); нужен extra «llm» (пакет openai).

Правило предметных ТЗ: модель не придумывает числа. explain() принимает ответ модели,
только если каждое его число есть в фактах; иначе, как и при сбое API, возвращает склейку
фактов и причину — числа всегда из детерминированного расчёта.
"""

from __future__ import annotations

import os
import re
from dataclasses import dataclass
from typing import Protocol

_NUMBER = re.compile(r"\d+(?:[.,]\d+)?")


class LanguageModel(Protocol):
    """Интерфейс внешней языковой модели."""

    name: str

    async def complete(self, system: str, facts: list[str]) -> str: ...


class JoinModel:
    """Заглушка: ответ = склейка фактов. Детерминированно, без сети и ключей."""

    name = "join"

    def __init__(self, sep: str = "; ") -> None:
        self._sep = sep

    async def complete(self, system: str, facts: list[str]) -> str:
        return self._sep.join(facts)


class OpenAICompatibleModel:
    """Живая модель через OpenAI-совместимый /chat/completions."""

    name = "openai-compatible"

    def __init__(
        self, base_url: str, api_key: str, model: str, timeout: float = 30.0, max_tokens: int = 400
    ) -> None:
        from openai import AsyncOpenAI

        self._client = AsyncOpenAI(base_url=base_url, api_key=api_key, timeout=timeout)
        self._model = model
        self._max_tokens = max_tokens

    async def complete(self, system: str, facts: list[str]) -> str:
        resp = await self._client.chat.completions.create(
            model=self._model,
            max_tokens=self._max_tokens,
            temperature=0,
            messages=[{"role": "system", "content": system}, {"role": "user", "content": "\n".join(facts)}],
        )
        return resp.choices[0].message.content or ""


def model_from_env() -> LanguageModel:
    """LLM_PROVIDER=join (по умолчанию) | openai (LLM_BASE_URL, LLM_API_KEY, LLM_MODEL)."""
    if os.environ.get("LLM_PROVIDER", "join") == "openai":
        return OpenAICompatibleModel(
            os.environ["LLM_BASE_URL"], os.environ["LLM_API_KEY"], os.environ.get("LLM_MODEL", "GigaChat")
        )
    return JoinModel()


def _canon(n: str) -> str:
    n = n.replace(",", ".")
    if "." in n:  # 75.50 → 75.5, 75.0 → 75; у целых нули не трогаем (100 ≠ 1)
        return n.rstrip("0").rstrip(".")
    return n.lstrip("0") or "0"


def numbers(text: str) -> set[str]:
    return {_canon(n) for n in _NUMBER.findall(text)}


def invented_numbers(answer: str, facts: list[str]) -> set[str]:
    """Числа ответа, которых нет в фактах."""
    return numbers(answer) - numbers(" ".join(facts))


@dataclass(frozen=True)
class Explanation:
    text: str
    source: str  # имя модели или "join (fallback: …)"


async def explain(model: LanguageModel, system: str, facts: list[str]) -> Explanation:
    """Объяснение от модели с защитой от выдуманных чисел и сбоев внешнего API."""
    fallback = JoinModel()
    try:
        answer = (await model.complete(system, facts)).strip()
    except Exception as exc:  # noqa: BLE001 — внешний API не должен ронять расчёт
        return Explanation(await fallback.complete(system, facts), f"join (fallback: {type(exc).__name__})")
    extra = invented_numbers(answer, facts)
    if not answer or extra:
        why = "empty answer" if not answer else f"invented numbers {sorted(extra)}"
        return Explanation(await fallback.complete(system, facts), f"join (fallback: {why})")
    return Explanation(answer, model.name)
