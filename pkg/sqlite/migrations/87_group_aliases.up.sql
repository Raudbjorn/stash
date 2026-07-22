CREATE TABLE `group_aliases` (
  `group_id` integer NOT NULL,
  `alias` varchar(255) NOT NULL,
  PRIMARY KEY(`group_id`, `alias`),
  foreign key(`group_id`) references `groups`(`id`) on delete CASCADE
);

CREATE INDEX `index_group_aliases_on_alias` on `group_aliases` (`alias`);

INSERT INTO `group_aliases` (`group_id`, `alias`)
  SELECT `id`, `aliases` FROM `groups`
  WHERE `aliases` IS NOT NULL AND `aliases` != '';

ALTER TABLE `groups` DROP COLUMN `aliases`;
