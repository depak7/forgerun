-- Claude Code, authenticated with a subscription token (`claude setup-token`).
-- Families are the CLI's aliases for the latest model of each kind; effort is
-- a separate CLI flag, stored here like Cursor's so the API offers it the same
-- way. The plain variant (effort '') leaves effort to the CLI's default.
-- Not ZDR: subscription traffic isn't zero-retention.
INSERT INTO agents (id, name) VALUES ('claude-code', 'Claude Code');

INSERT INTO models (id, agent_id, family, name, effort, thinking, fast, is_default, zdr) VALUES
    ('sonnet', 'claude-code', 'sonnet', 'Claude Sonnet (latest)', '', false, false, true, false),
    ('sonnet-low', 'claude-code', 'sonnet', 'Claude Sonnet (latest), low effort', 'low', false, false, false, false),
    ('sonnet-medium', 'claude-code', 'sonnet', 'Claude Sonnet (latest), medium effort', 'medium', false, false, false, false),
    ('sonnet-high', 'claude-code', 'sonnet', 'Claude Sonnet (latest), high effort', 'high', false, false, false, false),
    ('sonnet-xhigh', 'claude-code', 'sonnet', 'Claude Sonnet (latest), xhigh effort', 'xhigh', false, false, false, false),
    ('sonnet-max', 'claude-code', 'sonnet', 'Claude Sonnet (latest), max effort', 'max', false, false, false, false),
    ('opus', 'claude-code', 'opus', 'Claude Opus (latest)', '', false, false, true, false),
    ('opus-low', 'claude-code', 'opus', 'Claude Opus (latest), low effort', 'low', false, false, false, false),
    ('opus-medium', 'claude-code', 'opus', 'Claude Opus (latest), medium effort', 'medium', false, false, false, false),
    ('opus-high', 'claude-code', 'opus', 'Claude Opus (latest), high effort', 'high', false, false, false, false),
    ('opus-xhigh', 'claude-code', 'opus', 'Claude Opus (latest), xhigh effort', 'xhigh', false, false, false, false),
    ('opus-max', 'claude-code', 'opus', 'Claude Opus (latest), max effort', 'max', false, false, false, false),
    ('fable', 'claude-code', 'fable', 'Claude Fable (latest)', '', false, false, true, false),
    ('fable-low', 'claude-code', 'fable', 'Claude Fable (latest), low effort', 'low', false, false, false, false),
    ('fable-medium', 'claude-code', 'fable', 'Claude Fable (latest), medium effort', 'medium', false, false, false, false),
    ('fable-high', 'claude-code', 'fable', 'Claude Fable (latest), high effort', 'high', false, false, false, false),
    ('fable-xhigh', 'claude-code', 'fable', 'Claude Fable (latest), xhigh effort', 'xhigh', false, false, false, false),
    ('fable-max', 'claude-code', 'fable', 'Claude Fable (latest), max effort', 'max', false, false, false, false);

UPDATE agents SET default_model_id = 'sonnet' WHERE id = 'claude-code';
