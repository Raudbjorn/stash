CREATE TABLE `clips_view_dates` (
  `clip_id` integer not null,
  `view_date` datetime not null,
  foreign key(`clip_id`) references `clips`(`id`) on delete CASCADE
);

CREATE INDEX `index_clips_view_dates` ON `clips_view_dates` (`clip_id`);

CREATE TABLE `clips_o_dates` (
  `clip_id` integer not null,
  `o_date` datetime not null,
  foreign key(`clip_id`) references `clips`(`id`) on delete CASCADE
);

CREATE INDEX `index_clips_o_dates` ON `clips_o_dates` (`clip_id`);
