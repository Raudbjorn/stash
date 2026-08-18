-- Tie cached scene vectors to the exact multimodal input that produced them.
ALTER TABLE ai_scene_embeddings
    ADD COLUMN input_hash TEXT NOT NULL DEFAULT '';
