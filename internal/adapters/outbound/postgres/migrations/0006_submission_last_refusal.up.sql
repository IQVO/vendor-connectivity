-- The refusal a callee released that keeps a submission PROCESSING (ADR 0016): the Err
-- code, the neighbour's detail and the JSON path of the refusal that sent the last
-- attempt back to the queue. Empty on a new submission and cleared by the outcome.
ALTER TABLE submissions
    ADD COLUMN last_refusal_code    TEXT NOT NULL DEFAULT '',
    ADD COLUMN last_refusal_message TEXT NOT NULL DEFAULT '',
    ADD COLUMN last_refusal_path    TEXT NOT NULL DEFAULT '';
