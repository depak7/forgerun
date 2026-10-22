-- Models that run without their agent's credential, such as OpenCode's free
-- models. false (the default) keeps every existing model needing its key.
ALTER TABLE models ADD COLUMN keyless BOOLEAN NOT NULL DEFAULT false;

-- OpenCode, on OpenCode's own hosted models (opencode/<id>), seeded from
-- https://opencode.ai/inference/v1/models on 2026-10-22. Free models need no
-- key but only work from the OpenCode CLI, and may be withdrawn: switch one
-- off with UPDATE ... SET enabled = false. Paid models use OPENCODE_API_KEY.
-- None is ZDR: OpenCode doesn't promise zero retention for them.
INSERT INTO agents (id, name) VALUES ('opencode', 'OpenCode');

INSERT INTO models (id, agent_id, family, name, is_default, zdr, keyless) VALUES
    ('big-pickle', 'opencode', 'big-pickle', 'Big Pickle (free)', true, false, true),
    ('deepseek-v4.1-flash-free', 'opencode', 'deepseek-v4.1-flash-free', 'DeepSeek V4.1 Flash (free)', true, false, true),
    ('glm-5.3-flash-free', 'opencode', 'glm-5.3-flash-free', 'GLM 5.3 Flash (free)', true, false, true),
    ('minimax-m3.1-flash-free', 'opencode', 'minimax-m3.1-flash-free', 'MiniMax M3.1 Flash (free)', true, false, true),
    ('claude-sonnet-5-5', 'opencode', 'claude-sonnet-5-5', 'Claude Sonnet 5.5', true, false, false),
    ('claude-opus-5-5', 'opencode', 'claude-opus-5-5', 'Claude Opus 5.5', true, false, false),
    ('gpt-5.3-codex', 'opencode', 'gpt-5.3-codex', 'GPT-5.3 Codex', true, false, false),
    ('gemini-3.1-pro', 'opencode', 'gemini-3.1-pro', 'Gemini 3.1 Pro', true, false, false),
    ('kimi-k3', 'opencode', 'kimi-k3', 'Kimi K3', true, false, false),
    ('glm-5.3', 'opencode', 'glm-5.3', 'GLM 5.3', true, false, false),
    ('deepseek-v4-pro', 'opencode', 'deepseek-v4-pro', 'DeepSeek V4 Pro', true, false, false);

-- A free model by default, so OpenCode works with nothing configured.
UPDATE agents SET default_model_id = 'big-pickle' WHERE id = 'opencode';
