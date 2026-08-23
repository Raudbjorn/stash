UPDATE `saved_filters`
SET `find_filter` = json_set(`find_filter`, '$.sort', 'date')
WHERE `mode` = 'SCENES'
  AND `find_filter` IS NOT NULL
  AND `find_filter` <> ''
  AND json_valid(`find_filter`)
  AND json_extract(`find_filter`, '$.sort') IN ('interactive', 'interactive_speed');

ALTER TABLE `video_files` DROP COLUMN `interactive`;
ALTER TABLE `video_files` DROP COLUMN `interactive_speed`;
DROP TABLE IF EXISTS `funscript_index`;
