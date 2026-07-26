-- flag markers created automatically (e.g. by ffmpeg scene-cut detection),
-- so they can be filtered/bulk-managed separately from user-created markers
ALTER TABLE `scene_markers` ADD COLUMN `auto_generated` boolean not null default '0';
