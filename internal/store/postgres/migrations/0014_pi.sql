-- pi, a coding agent that works with many providers. A model's family is
-- "<provider>/<model>" in pi's own names (from `pi --list-models`, pi 1.5.0),
-- and its provider decides the key it needs: OPENROUTER_API_KEY,
-- OPENAI_API_KEY or ANTHROPIC_API_KEY (internal/agent/pi.go). Effort is pi's
-- thinking level; the plain variant leaves it to pi. Add a model with an
-- INSERT here and nowhere else; a new provider also needs a line in pi.go.
-- None is ZDR, and none runs without its provider's key.
INSERT INTO agents (id, name) VALUES ('pi', 'pi');

INSERT INTO models (id, agent_id, family, name, effort, thinking, fast, is_default, zdr) VALUES
    ('openrouter/anthropic/claude-sonnet-5.5', 'pi', 'openrouter/anthropic/claude-sonnet-5.5', 'Claude Sonnet 5.5 (OpenRouter)', '', false, false, true, false),
    ('openrouter/anthropic/claude-sonnet-5.5-low', 'pi', 'openrouter/anthropic/claude-sonnet-5.5', 'Claude Sonnet 5.5 (OpenRouter), low thinking', 'low', false, false, false, false),
    ('openrouter/anthropic/claude-sonnet-5.5-medium', 'pi', 'openrouter/anthropic/claude-sonnet-5.5', 'Claude Sonnet 5.5 (OpenRouter), medium thinking', 'medium', false, false, false, false),
    ('openrouter/anthropic/claude-sonnet-5.5-high', 'pi', 'openrouter/anthropic/claude-sonnet-5.5', 'Claude Sonnet 5.5 (OpenRouter), high thinking', 'high', false, false, false, false),
    ('openrouter/anthropic/claude-opus-5.5', 'pi', 'openrouter/anthropic/claude-opus-5.5', 'Claude Opus 5.5 (OpenRouter)', '', false, false, true, false),
    ('openrouter/anthropic/claude-opus-5.5-low', 'pi', 'openrouter/anthropic/claude-opus-5.5', 'Claude Opus 5.5 (OpenRouter), low thinking', 'low', false, false, false, false),
    ('openrouter/anthropic/claude-opus-5.5-medium', 'pi', 'openrouter/anthropic/claude-opus-5.5', 'Claude Opus 5.5 (OpenRouter), medium thinking', 'medium', false, false, false, false),
    ('openrouter/anthropic/claude-opus-5.5-high', 'pi', 'openrouter/anthropic/claude-opus-5.5', 'Claude Opus 5.5 (OpenRouter), high thinking', 'high', false, false, false, false),
    ('openrouter/openai/gpt-5.5', 'pi', 'openrouter/openai/gpt-5.5', 'GPT-5.5 (OpenRouter)', '', false, false, true, false),
    ('openrouter/openai/gpt-5.5-low', 'pi', 'openrouter/openai/gpt-5.5', 'GPT-5.5 (OpenRouter), low thinking', 'low', false, false, false, false),
    ('openrouter/openai/gpt-5.5-medium', 'pi', 'openrouter/openai/gpt-5.5', 'GPT-5.5 (OpenRouter), medium thinking', 'medium', false, false, false, false),
    ('openrouter/openai/gpt-5.5-high', 'pi', 'openrouter/openai/gpt-5.5', 'GPT-5.5 (OpenRouter), high thinking', 'high', false, false, false, false),
    ('openrouter/deepseek/deepseek-v4-pro', 'pi', 'openrouter/deepseek/deepseek-v4-pro', 'DeepSeek V4 Pro (OpenRouter)', '', false, false, true, false),
    ('openrouter/deepseek/deepseek-v4-pro-low', 'pi', 'openrouter/deepseek/deepseek-v4-pro', 'DeepSeek V4 Pro (OpenRouter), low thinking', 'low', false, false, false, false),
    ('openrouter/deepseek/deepseek-v4-pro-medium', 'pi', 'openrouter/deepseek/deepseek-v4-pro', 'DeepSeek V4 Pro (OpenRouter), medium thinking', 'medium', false, false, false, false),
    ('openrouter/deepseek/deepseek-v4-pro-high', 'pi', 'openrouter/deepseek/deepseek-v4-pro', 'DeepSeek V4 Pro (OpenRouter), high thinking', 'high', false, false, false, false),
    ('openai/gpt-5.5', 'pi', 'openai/gpt-5.5', 'GPT-5.5 (OpenAI)', '', false, false, true, false),
    ('openai/gpt-5.5-low', 'pi', 'openai/gpt-5.5', 'GPT-5.5 (OpenAI), low thinking', 'low', false, false, false, false),
    ('openai/gpt-5.5-medium', 'pi', 'openai/gpt-5.5', 'GPT-5.5 (OpenAI), medium thinking', 'medium', false, false, false, false),
    ('openai/gpt-5.5-high', 'pi', 'openai/gpt-5.5', 'GPT-5.5 (OpenAI), high thinking', 'high', false, false, false, false),
    ('openai/gpt-5.3-codex', 'pi', 'openai/gpt-5.3-codex', 'GPT-5.3 Codex (OpenAI)', '', false, false, true, false),
    ('openai/gpt-5.3-codex-low', 'pi', 'openai/gpt-5.3-codex', 'GPT-5.3 Codex (OpenAI), low thinking', 'low', false, false, false, false),
    ('openai/gpt-5.3-codex-medium', 'pi', 'openai/gpt-5.3-codex', 'GPT-5.3 Codex (OpenAI), medium thinking', 'medium', false, false, false, false),
    ('openai/gpt-5.3-codex-high', 'pi', 'openai/gpt-5.3-codex', 'GPT-5.3 Codex (OpenAI), high thinking', 'high', false, false, false, false),
    ('anthropic/claude-sonnet-5-5', 'pi', 'anthropic/claude-sonnet-5-5', 'Claude Sonnet 5.5 (Anthropic)', '', false, false, true, false),
    ('anthropic/claude-sonnet-5-5-low', 'pi', 'anthropic/claude-sonnet-5-5', 'Claude Sonnet 5.5 (Anthropic), low thinking', 'low', false, false, false, false),
    ('anthropic/claude-sonnet-5-5-medium', 'pi', 'anthropic/claude-sonnet-5-5', 'Claude Sonnet 5.5 (Anthropic), medium thinking', 'medium', false, false, false, false),
    ('anthropic/claude-sonnet-5-5-high', 'pi', 'anthropic/claude-sonnet-5-5', 'Claude Sonnet 5.5 (Anthropic), high thinking', 'high', false, false, false, false),
    ('anthropic/claude-opus-5-5', 'pi', 'anthropic/claude-opus-5-5', 'Claude Opus 5.5 (Anthropic)', '', false, false, true, false),
    ('anthropic/claude-opus-5-5-low', 'pi', 'anthropic/claude-opus-5-5', 'Claude Opus 5.5 (Anthropic), low thinking', 'low', false, false, false, false),
    ('anthropic/claude-opus-5-5-medium', 'pi', 'anthropic/claude-opus-5-5', 'Claude Opus 5.5 (Anthropic), medium thinking', 'medium', false, false, false, false),
    ('anthropic/claude-opus-5-5-high', 'pi', 'anthropic/claude-opus-5-5', 'Claude Opus 5.5 (Anthropic), high thinking', 'high', false, false, false, false);

UPDATE agents SET default_model_id = 'openrouter/anthropic/claude-sonnet-5.5' WHERE id = 'pi';
