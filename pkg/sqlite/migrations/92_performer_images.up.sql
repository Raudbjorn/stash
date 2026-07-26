PRAGMA foreign_keys=OFF;
CREATE TABLE `performer_images` (
  `performer_id` integer NOT NULL,
  `position` integer NOT NULL,
  `image_blob` varchar(255) NOT NULL REFERENCES `blobs`(`checksum`),
  foreign key(`performer_id`) references `performers`(`id`) on delete CASCADE,
  PRIMARY KEY(`performer_id`, `position`, `image_blob`)
);
CREATE INDEX `performers_images_image_blob` on `performer_images` (`image_blob`);
INSERT INTO `performer_images` (`performer_id`,`position`,`image_blob`)
  SELECT `id`,'0',`image_blob` FROM `performers`
  WHERE `performers`.`image_blob` IS NOT NULL AND `performers`.`image_blob` != '';
PRAGMA foreign_keys=ON;
