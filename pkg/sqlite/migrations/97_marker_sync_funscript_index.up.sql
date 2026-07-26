-- Native Marker Sync: local funscript index.
-- Mirrors the timestamp.trade plugin's `script_index` table (previously kept in a
-- separate sqlite file) so funscript matching/submission is first-class.
CREATE TABLE `funscript_index` (
  `id` integer NOT NULL PRIMARY KEY AUTOINCREMENT,
  `filename` varchar(510) NOT NULL,
  `metadata` text,
  `scene_id` integer DEFAULT NULL REFERENCES `scenes`(`id`) ON DELETE SET NULL,
  `md5` varchar(255) NOT NULL
);
CREATE UNIQUE INDEX `index_funscript_index_filename` ON `funscript_index` (`filename`);
CREATE INDEX `index_funscript_index_md5` ON `funscript_index` (`md5`);
CREATE INDEX `index_funscript_index_scene_id` ON `funscript_index` (`scene_id`);
