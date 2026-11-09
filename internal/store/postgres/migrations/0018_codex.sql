-- Codex, OpenAI's CLI, on OpenAI's API with OPENAI_API_KEY (the variable
-- pi's OpenAI models already use; each VM holds only a placeholder).
-- Families are the CLI's model slugs from the model catalog built into
-- codex-cli 0.167.0 (gpt-6.1-sol the workhorse, gpt-6-astra the frontier,
-- gpt-6-luna the fast one; older generations and the hidden review/cyber
-- models left out, and gpt-6.1-terra too: it is positioned for writing,
-- visual and computer-use work, not coding). Effort is its model_reasoning_effort level, stored like
-- Claude Code's so the API offers it the same way; the plain variant
-- (effort '') leaves it to the CLI's default for the model. low..max were
-- verified against codex-cli 0.167.0 for all three families (the CLI sends
-- each verbatim in reasoning.effort); 'ultra' is clamped client-side (to
-- xhigh for sol/astra, max for luna) and delegates to subagents, which
-- forgerun doesn't run, so there are no 'ultra' rows. Real availability of
-- a level still depends on the model and the account's plan (OpenAI's
-- docs); the CLI accepts the level and the API reports an unsupported one.
-- None is ZDR: OpenAI's retention depends on the API account's terms.
INSERT INTO agents (id, name) VALUES ('codex', 'Codex');

INSERT INTO models (id, agent_id, family, name, effort, thinking, fast, is_default, zdr) VALUES
    ('gpt-6.1-sol', 'codex', 'gpt-6.1-sol', 'GPT-6.1 Sol', '', false, false, true, false),
    ('gpt-6.1-sol-low', 'codex', 'gpt-6.1-sol', 'GPT-6.1 Sol, low effort', 'low', false, false, false, false),
    ('gpt-6.1-sol-medium', 'codex', 'gpt-6.1-sol', 'GPT-6.1 Sol, medium effort', 'medium', false, false, false, false),
    ('gpt-6.1-sol-high', 'codex', 'gpt-6.1-sol', 'GPT-6.1 Sol, high effort', 'high', false, false, false, false),
    ('gpt-6.1-sol-xhigh', 'codex', 'gpt-6.1-sol', 'GPT-6.1 Sol, xhigh effort', 'xhigh', false, false, false, false),
    ('gpt-6.1-sol-max', 'codex', 'gpt-6.1-sol', 'GPT-6.1 Sol, max effort', 'max', false, false, false, false),
    ('gpt-6-astra', 'codex', 'gpt-6-astra', 'GPT-6 Astra', '', false, false, true, false),
    ('gpt-6-astra-low', 'codex', 'gpt-6-astra', 'GPT-6 Astra, low effort', 'low', false, false, false, false),
    ('gpt-6-astra-medium', 'codex', 'gpt-6-astra', 'GPT-6 Astra, medium effort', 'medium', false, false, false, false),
    ('gpt-6-astra-high', 'codex', 'gpt-6-astra', 'GPT-6 Astra, high effort', 'high', false, false, false, false),
    ('gpt-6-astra-xhigh', 'codex', 'gpt-6-astra', 'GPT-6 Astra, xhigh effort', 'xhigh', false, false, false, false),
    ('gpt-6-astra-max', 'codex', 'gpt-6-astra', 'GPT-6 Astra, max effort', 'max', false, false, false, false),
    ('gpt-6-luna', 'codex', 'gpt-6-luna', 'GPT-6 Luna', '', false, false, true, false),
    ('gpt-6-luna-low', 'codex', 'gpt-6-luna', 'GPT-6 Luna, low effort', 'low', false, false, false, false),
    ('gpt-6-luna-medium', 'codex', 'gpt-6-luna', 'GPT-6 Luna, medium effort', 'medium', false, false, false, false),
    ('gpt-6-luna-high', 'codex', 'gpt-6-luna', 'GPT-6 Luna, high effort', 'high', false, false, false, false),
    ('gpt-6-luna-xhigh', 'codex', 'gpt-6-luna', 'GPT-6 Luna, xhigh effort', 'xhigh', false, false, false, false),
    ('gpt-6-luna-max', 'codex', 'gpt-6-luna', 'GPT-6 Luna, max effort', 'max', false, false, false, false);

UPDATE agents SET default_model_id = 'gpt-6.1-sol' WHERE id = 'codex';
