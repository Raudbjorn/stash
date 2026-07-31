#!/usr/bin/env python3
"""Reference postprocessing, extracted from /opt/nsfw-ai-server.

This is the ORIGINAL implementation, copied verbatim rather than reimplemented,
so the Go port has something to be diffed against. It reads a case description
on stdin and writes the reference output on stdout; the Go test runs it and
compares.

Extracted rather than imported because the server needs its whole dependency
tree (torch, fastapi) to import anything, and the two functions being validated
depend on none of it.
"""
import json
import sys


# ---------------------------------------------------------------- stage one --
# AI_VideoResult._mutate_server_result_tags, verbatim.

class TagTimeFrame:
    def __init__(self, start, end=None, confidence=None):
        self.start = start
        self.end = end
        self.confidence = confidence


def mutate_server_result_tags(frames, frame_interval, max_merge_seconds=0):
    toReturn = {}
    for frame in frames:
        frame_index = frame['frame_index']
        for key, value in frame.items():
            if key != "frame_index":
                currentCategoryDict = None
                if not isinstance(value, list):
                    raise Exception(f"Category {key} is not a list")
                if key in toReturn:
                    currentCategoryDict = toReturn[key]
                else:
                    currentCategoryDict = {}
                    toReturn[key] = currentCategoryDict

                for item in value:
                    if isinstance(item, (tuple, list)):
                        tag_name, confidence = item
                    else:
                        tag_name = item
                        confidence = None

                    if tag_name not in currentCategoryDict:
                        currentCategoryDict[tag_name] = [
                            TagTimeFrame(start=frame_index, end=None, confidence=confidence)]
                    else:
                        last_time_frame = currentCategoryDict[tag_name][-1]

                        if last_time_frame.end is None:
                            if (frame_index - last_time_frame.start - frame_interval <= max_merge_seconds
                                    and last_time_frame.confidence == confidence):
                                last_time_frame.end = frame_index
                            else:
                                currentCategoryDict[tag_name].append(
                                    TagTimeFrame(start=frame_index, end=None, confidence=confidence))
                        elif (frame_index - last_time_frame.end - frame_interval <= max_merge_seconds
                              and last_time_frame.confidence == confidence):
                            last_time_frame.end = frame_index
                        else:
                            currentCategoryDict[tag_name].append(
                                TagTimeFrame(start=frame_index, end=None, confidence=confidence))

    return toReturn


# ---------------------------------------------------------------- stage two --
# tag_models.TimeFrame and compute_video_timespans_clustering, verbatim.

class TimeFrame:
    def __init__(self, start, end, totalConfidence):
        self.start = start
        self.end = end
        self.totalConfidence = totalConfidence

    def get_density(self, frame_interval):
        return self.totalConfidence / (self.get_duration(frame_interval))

    def get_duration(self, frame_interval):
        return (self.end - self.start) + frame_interval

    def merge(self, new_start, new_end, new_confidence, frame_interval):
        self.start = min(self.start, new_start)
        self.end = max(self.end, new_end)
        self.totalConfidence += new_confidence * (self.get_duration(frame_interval))


def format_duration_or_percent(value, video_duration):
    try:
        if isinstance(value, float):
            return value
        elif isinstance(value, str):
            if value.endswith('%'):
                return float(value[:-1]) / 100 * video_duration
            elif value.endswith('s'):
                return float(value[:-1])
            else:
                return float(value)
        elif isinstance(value, int):
            return float(value)
    except Exception:
        return 0.0


def get_or_default(d, key, default, csv_defaults):
    if key in d and d[key]:
        return d[key]
    return csv_defaults.get(key, default)


def compute_video_timespans_clustering(timespans, video_duration, frame_interval,
                                       category_config, csv_defaults,
                                       density_weight, gap_factor, average_factor, min_gap):
    toReturn = {}

    for category, tag_raw_timespans in timespans.items():
        if category not in category_config:
            continue

        toReturn[category] = {}

        for tag, raw_timespans in tag_raw_timespans.items():
            if tag not in category_config[category]:
                continue

            tag_threshold = float(get_or_default(
                category_config[category][tag], 'TagThreshold', 0.5, csv_defaults))
            renamed_tag = category_config[category][tag]['RenamedTag']
            tag_min_duration = format_duration_or_percent(
                get_or_default(category_config[category][tag], 'MinMarkerDuration', 12, csv_defaults),
                video_duration)
            if tag_min_duration <= 0:
                continue

            initial_buckets = []
            current_bucket = None
            for raw_timespan in raw_timespans:
                confidence = raw_timespan.confidence
                # The Go port treats an absent confidence as passing, because a
                # provider that omits one has already applied its own threshold.
                # Reproduced here so the two agree.
                effective = 1.0 if confidence is None else confidence
                if effective < tag_threshold:
                    continue

                start = raw_timespan.start
                end = raw_timespan.end if raw_timespan.end is not None else raw_timespan.start

                duration = (end - start) + frame_interval
                if current_bucket is None:
                    current_bucket = TimeFrame(start=start, end=end, totalConfidence=effective * duration)
                else:
                    if start - current_bucket.end == frame_interval:
                        current_bucket.merge(start, end, effective, frame_interval)
                    else:
                        initial_buckets.append(current_bucket)
                        current_bucket = TimeFrame(start=start, end=end, totalConfidence=effective * duration)
            if current_bucket is not None:
                initial_buckets.append(current_bucket)

            def should_merge(current_bucket, next_bucket, frame_interval):
                gap = next_bucket.start - current_bucket.end - frame_interval
                duration_current = current_bucket.get_duration(frame_interval)
                duration_next = next_bucket.get_duration(frame_interval)
                density_current = current_bucket.get_density(frame_interval)
                density_next = next_bucket.get_density(frame_interval)

                weighted_duration_current = duration_current * (1 + density_weight * density_current)
                weighted_duration_next = duration_next * (1 + density_weight * density_next)

                weighted_diff = abs(weighted_duration_current - weighted_duration_next)
                return (gap <= min_gap + (min(weighted_duration_current, weighted_duration_next)
                                          + weighted_diff * average_factor) * gap_factor)

            merged_buckets = initial_buckets
            merging_occurred = True
            max_iterations = 10
            iterations = 0
            while merging_occurred and iterations < max_iterations:
                merging_occurred = False
                new_buckets = []
                i = 0
                iterations += 1

                while i < len(merged_buckets):
                    if i < len(merged_buckets) - 1 and should_merge(
                            merged_buckets[i], merged_buckets[i + 1], frame_interval):
                        current_bucket = merged_buckets[i]
                        next_bucket = merged_buckets[i + 1]
                        new_bucket = TimeFrame(
                            start=current_bucket.start,
                            end=next_bucket.end,
                            totalConfidence=current_bucket.totalConfidence + next_bucket.totalConfidence,
                        )
                        new_buckets.append(new_bucket)
                        i += 2
                        merging_occurred = True
                    else:
                        new_buckets.append(merged_buckets[i])
                        i += 1
                merged_buckets = new_buckets

            final_buckets = [
                bucket for bucket in merged_buckets
                if (bucket.get_duration(frame_interval)) >= tag_min_duration
            ]

            toReturn[category][renamed_tag] = final_buckets

    return toReturn


# ------------------------------------------------------------------- driver --

def main():
    case = json.load(sys.stdin)

    frames = case["frames"]
    frame_interval = case["frame_interval"]
    max_merge = case.get("max_merge_seconds", 0)

    collapsed = mutate_server_result_tags(frames, frame_interval, max_merge)

    spans_out = {}
    for category, by_tag in collapsed.items():
        spans_out[category] = {}
        for tag, spans in by_tag.items():
            spans_out[category][tag] = [
                {"start": s.start, "end": s.end, "confidence": s.confidence} for s in spans
            ]

    markers_out = []
    if case.get("cluster"):
        params = case["params"]
        clustered = compute_video_timespans_clustering(
            collapsed,
            case["duration"],
            frame_interval,
            case["category_config"],
            case.get("csv_defaults", {}),
            params["density_weight"],
            params["gap_factor"],
            params["average_factor"],
            params["min_gap"],
        )
        for category in sorted(clustered):
            for renamed in sorted(clustered[category]):
                for bucket in clustered[category][renamed]:
                    markers_out.append({
                        "category": category,
                        "renamed_tag": renamed,
                        "start": bucket.start,
                        "end": bucket.end,
                        "total_confidence": bucket.totalConfidence,
                    })
        markers_out.sort(key=lambda m: (m["start"], m["renamed_tag"]))

    json.dump({"spans": spans_out, "markers": markers_out}, sys.stdout)


if __name__ == "__main__":
    main()
