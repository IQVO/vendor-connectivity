-- The pending refusal of a submission (ADR 0016), reversed.

ALTER TABLE submissions
    DROP COLUMN IF EXISTS last_refusal_path,
    DROP COLUMN IF EXISTS last_refusal_message,
    DROP COLUMN IF EXISTS last_refusal_code;
