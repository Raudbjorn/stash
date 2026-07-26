CREATE TABLE `clips` (
  `id` integer not null primary key autoincrement,
  `title` varchar(255),
  `scene_id` integer not null,
  `seconds` float not null,
  `end_seconds` float,
  `rating` tinyint,
  `created_at` datetime not null,
  `updated_at` datetime not null,
  foreign key(`scene_id`) references `scenes`(`id`) on delete CASCADE
);

CREATE INDEX `index_clips_on_scene_id` on `clips` (`scene_id`);

CREATE TABLE `clips_tags` (
  `clip_id` integer not null,
  `tag_id` integer not null,
  foreign key(`clip_id`) references `clips`(`id`) on delete CASCADE,
  foreign key(`tag_id`) references `tags`(`id`) on delete CASCADE,
  PRIMARY KEY(`clip_id`, `tag_id`)
);

CREATE INDEX `index_clips_tags_on_tag_id` on `clips_tags` (`tag_id`);
CREATE INDEX `index_clips_tags_on_clip_id` on `clips_tags` (`clip_id`);
