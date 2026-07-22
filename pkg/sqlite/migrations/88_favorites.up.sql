-- add favorite support to scenes, images and galleries, mirroring performers
ALTER TABLE `scenes` ADD COLUMN `favorite` boolean not null default '0';
ALTER TABLE `images` ADD COLUMN `favorite` boolean not null default '0';
ALTER TABLE `galleries` ADD COLUMN `favorite` boolean not null default '0';
