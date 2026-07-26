-- expose the video container's own embedded creation timestamp, if present
ALTER TABLE `video_files` ADD COLUMN `creation_time` datetime;
