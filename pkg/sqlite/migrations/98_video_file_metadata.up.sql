-- persist bounded container metadata already returned by the normal ffprobe scan
ALTER TABLE `video_files` ADD COLUMN `title` text;
ALTER TABLE `video_files` ADD COLUMN `comment` text;
ALTER TABLE `video_files` ADD COLUMN `encoder` text;
ALTER TABLE `video_files` ADD COLUMN `tags` text;
ALTER TABLE `video_files` ADD COLUMN `metadata_probed` boolean NOT NULL DEFAULT 0;
