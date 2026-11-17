-- prime-agent (Prime Intellect). A model's family is "<provider>/<model>" in
-- prime-agent's own names (from its bundled catalog / --list-models). The
-- provider decides the key: ANTHROPIC_API_KEY, OPENAI_API_KEY,
-- OPENROUTER_API_KEY, GEMINI_API_KEY, XAI_API_KEY or PRIME_API_KEY
-- (internal/agent/prime.go). Effort is prime-agent's thinking level; the
-- plain variant leaves it to the agent. Add a model with an INSERT here;
-- a new provider also needs a line in prime.go. None is ZDR, and none runs
-- without its provider's key.
INSERT INTO agents (id, name) VALUES ('prime', 'Prime Agent');

INSERT INTO models (id, agent_id, family, name, effort, thinking, fast, is_default, zdr) VALUES
    ('openrouter/anthropic/claude-sonnet-4.5', 'prime', 'openrouter/anthropic/claude-sonnet-4.5', 'Claude Sonnet 4.5 (OpenRouter)', '', false, false, true, false),
    ('openrouter/anthropic/claude-sonnet-4.5-low', 'prime', 'openrouter/anthropic/claude-sonnet-4.5', 'Claude Sonnet 4.5 (OpenRouter), low thinking', 'low', false, false, false, false),
    ('openrouter/anthropic/claude-sonnet-4.5-medium', 'prime', 'openrouter/anthropic/claude-sonnet-4.5', 'Claude Sonnet 4.5 (OpenRouter), medium thinking', 'medium', false, false, false, false),
    ('openrouter/anthropic/claude-sonnet-4.5-high', 'prime', 'openrouter/anthropic/claude-sonnet-4.5', 'Claude Sonnet 4.5 (OpenRouter), high thinking', 'high', false, false, false, false),
    ('openrouter/anthropic/claude-opus-4.6', 'prime', 'openrouter/anthropic/claude-opus-4.6', 'Claude Opus 4.6 (OpenRouter)', '', false, false, true, false),
    ('openrouter/anthropic/claude-opus-4.6-high', 'prime', 'openrouter/anthropic/claude-opus-4.6', 'Claude Opus 4.6 (OpenRouter), high thinking', 'high', false, false, false, false),
    ('openrouter/openai/gpt-5.5', 'prime', 'openrouter/openai/gpt-5.5', 'GPT-5.5 (OpenRouter)', '', false, false, true, false),
    ('openrouter/openai/gpt-5.5-high', 'prime', 'openrouter/openai/gpt-5.5', 'GPT-5.5 (OpenRouter), high thinking', 'high', false, false, false, false),
    ('openrouter/z-ai/glm-5.3', 'prime', 'openrouter/z-ai/glm-5.3', 'GLM 5.3 (OpenRouter)', '', false, false, true, false),
    ('openai/gpt-5.5', 'prime', 'openai/gpt-5.5', 'GPT-5.5 (OpenAI)', '', false, false, true, false),
    ('openai/gpt-5.5-high', 'prime', 'openai/gpt-5.5', 'GPT-5.5 (OpenAI), high thinking', 'high', false, false, false, false),
    ('openai/gpt-5.3-codex', 'prime', 'openai/gpt-5.3-codex', 'GPT-5.3 Codex (OpenAI)', '', false, false, true, false),
    ('anthropic/claude-sonnet-4-5', 'prime', 'anthropic/claude-sonnet-4-5', 'Claude Sonnet 4.5 (Anthropic)', '', false, false, true, false),
    ('anthropic/claude-sonnet-4-5-high', 'prime', 'anthropic/claude-sonnet-4-5', 'Claude Sonnet 4.5 (Anthropic), high thinking', 'high', false, false, false, false),
    ('anthropic/claude-opus-4-6', 'prime', 'anthropic/claude-opus-4-6', 'Claude Opus 4.6 (Anthropic)', '', false, false, true, false),
    ('anthropic/claude-opus-4-6-high', 'prime', 'anthropic/claude-opus-4-6', 'Claude Opus 4.6 (Anthropic), high thinking', 'high', false, false, false, false),
    ('google/gemini-2.5-pro', 'prime', 'google/gemini-2.5-pro', 'Gemini 2.5 Pro (Google)', '', false, false, true, false),
    ('google/gemini-2.5-flash', 'prime', 'google/gemini-2.5-flash', 'Gemini 2.5 Flash (Google)', '', false, false, true, false),
    ('xai/grok-code-fast-1', 'prime', 'xai/grok-code-fast-1', 'Grok Code Fast 1 (xAI)', '', false, false, true, false),
    ('xai/grok-4.6', 'prime', 'xai/grok-4.6', 'Grok 4.6 (xAI)', '', false, false, true, false),
    ('xai/grok-4.6-high', 'prime', 'xai/grok-4.6', 'Grok 4.6 (xAI), high thinking', 'high', false, false, false, false),
    ('prime-inference/z-ai/glm-5.3', 'prime', 'prime-inference/z-ai/glm-5.3', 'GLM 5.3 (Prime Inference)', '', false, false, true, false),
    ('prime-inference/z-ai/glm-5.3-high', 'prime', 'prime-inference/z-ai/glm-5.3', 'GLM 5.3 (Prime Inference), high thinking', 'high', false, false, false, false),
    ('prime-inference/anthropic/claude-sonnet-4.5', 'prime', 'prime-inference/anthropic/claude-sonnet-4.5', 'Claude Sonnet 4.5 (Prime Inference)', '', false, false, true, false),
    ('prime-inference/anthropic/claude-sonnet-4.5-high', 'prime', 'prime-inference/anthropic/claude-sonnet-4.5', 'Claude Sonnet 4.5 (Prime Inference), high thinking', 'high', false, false, false, false);

UPDATE agents SET default_model_id = 'openrouter/anthropic/claude-sonnet-4.5' WHERE id = 'prime';
