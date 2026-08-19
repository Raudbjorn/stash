CREATE TABLE `scene_metadata_plans` (
  `run_id` text NOT NULL,
  `scene_id` integer NOT NULL,
  `proposal_json` text NOT NULL,
  `model_fingerprint` text NOT NULL,
  `policy_version` text NOT NULL,
  `state` text NOT NULL,
  `created_at` datetime NOT NULL,
  `applied_at` datetime,
  PRIMARY KEY (`run_id`, `scene_id`),
  FOREIGN KEY (`scene_id`) REFERENCES `scenes` (`id`) ON DELETE CASCADE
);

CREATE TABLE `scene_metadata_plan_actions` (
  `id` integer PRIMARY KEY AUTOINCREMENT,
  `run_id` text NOT NULL,
  `scene_id` integer NOT NULL,
  `kind` text NOT NULL,
  `payload_json` text NOT NULL,
  `state` text NOT NULL,
  `reason_codes` text NOT NULL,
  FOREIGN KEY (`run_id`, `scene_id`) REFERENCES `scene_metadata_plans` (`run_id`, `scene_id`) ON DELETE CASCADE
);

CREATE INDEX `index_scene_metadata_plans_scene_state` ON `scene_metadata_plans` (`scene_id`, `state`);
CREATE INDEX `index_scene_metadata_plan_actions_plan_state` ON `scene_metadata_plan_actions` (`run_id`, `scene_id`, `state`);
