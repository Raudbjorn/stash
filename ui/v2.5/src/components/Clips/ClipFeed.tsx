import React, { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Link, useHistory } from "react-router-dom";
import { Button } from "react-bootstrap";
import { Icon } from "src/components/Shared/Icon";
import { LoadingIndicator } from "src/components/Shared/LoadingIndicator";
import {
  faVolumeHigh,
  faVolumeXmark,
  faArrowLeft,
} from "@fortawesome/free-solid-svg-icons";
import {
  useFindClips,
  useClipAddO,
  useClipAddPlay,
} from "src/core/StashService";
import * as GQL from "src/core/generated-graphql";
import TextUtils from "src/utils/text";

// Fisher–Yates shuffle producing a new array. Seeded by the initial fetch so the
// order is stable for the lifetime of the mount.
function shuffled<T>(arr: T[]): T[] {
  const a = arr.slice();
  for (let i = a.length - 1; i > 0; i--) {
    const j = Math.floor(Math.random() * (i + 1));
    [a[i], a[j]] = [a[j], a[i]];
  }
  return a;
}

interface IFeedItemProps {
  clip: GQL.SlimClipDataFragment;
  muted: boolean;
  onToggleMute: () => void;
  active: boolean;
  registerRef: (el: HTMLDivElement | null) => void;
  onPlayed: (id: string) => void;
}

const ClipFeedItem: React.FC<IFeedItemProps> = ({
  clip,
  muted,
  onToggleMute,
  active,
  registerRef,
  onPlayed,
}) => {
  const videoRef = useRef<HTMLVideoElement | null>(null);
  const [failed, setFailed] = useState(false);
  const [addO] = useClipAddO();

  // Play/pause the video as this item enters/leaves the viewport.
  useEffect(() => {
    const v = videoRef.current;
    if (!v) return;
    if (active && !failed) {
      v.play()
        .then(() => onPlayed(clip.id))
        .catch(() => {
          /* autoplay may be blocked until user interacts; ignore */
        });
    } else {
      v.pause();
    }
  }, [active, failed, clip.id, onPlayed]);

  const duration =
    clip.end_seconds != null ? clip.end_seconds - clip.seconds : undefined;

  return (
    <div className="clip-feed-item" ref={registerRef}>
      {!failed ? (
        <video
          ref={videoRef}
          className="clip-feed-video"
          src={clip.stream}
          muted={muted}
          loop
          playsInline
          preload="metadata"
          onError={() => setFailed(true)}
          onClick={() => {
            const v = videoRef.current;
            if (!v) return;
            if (v.paused) v.play().catch(() => {});
            else v.pause();
          }}
        />
      ) : (
        <div className="clip-feed-missing">
          <p>This clip has no generated video yet.</p>
          <Link to={`/clips/${clip.id}`}>Open clip to generate</Link>
        </div>
      )}

      <div className="clip-feed-overlay">
        <div className="clip-feed-info">
          <Link to={`/clips/${clip.id}`} className="clip-feed-title">
            {clip.title || "Untitled clip"}
          </Link>
          <Link to={`/scenes/${clip.scene_id}`} className="clip-feed-scene">
            View scene
          </Link>
          {duration !== undefined && (
            <span className="clip-feed-duration">
              {TextUtils.secondsToTimestamp(duration)}
            </span>
          )}
          {clip.tags.length > 0 && (
            <div className="clip-feed-tags">
              {clip.tags.map((t) => (
                <Link key={t.id} to={`/tags/${t.id}`} className="clip-feed-tag">
                  {t.name}
                </Link>
              ))}
            </div>
          )}
        </div>

        <div className="clip-feed-actions">
          <Button
            variant="minimal"
            className="clip-feed-btn"
            title="Mute / unmute"
            onClick={onToggleMute}
          >
            <Icon icon={muted ? faVolumeXmark : faVolumeHigh} />
          </Button>
          <Button
            variant="minimal"
            className="clip-feed-btn"
            title="O-counter"
            onClick={() => addO({ variables: { id: clip.id } })}
          >
            <span className="clip-feed-o">❤ {clip.o_counter ?? 0}</span>
          </Button>
        </div>
      </div>
    </div>
  );
};

const ClipFeed: React.FC = () => {
  const history = useHistory();
  const { data, loading } = useFindClips({
    filter: {
      per_page: 50,
      page: 1,
      sort: "created_at",
      direction: GQL.SortDirectionEnum.Desc,
    },
  });

  const [muted, setMuted] = useState(true);
  const [activeIndex, setActiveIndex] = useState(0);
  const [addPlay] = useClipAddPlay();
  const playedRef = useRef<Set<string>>(new Set());
  const itemRefs = useRef<(HTMLDivElement | null)[]>([]);

  const clips = useMemo(
    () => shuffled(data?.findClips.clips ?? []),
    [data?.findClips.clips]
  );

  // Record a play at most once per clip per feed session.
  const onPlayed = useCallback(
    (id: string) => {
      if (playedRef.current.has(id)) return;
      playedRef.current.add(id);
      addPlay({ variables: { id } }).catch(() => {});
    },
    [addPlay]
  );

  // Observe which item is centered in the viewport and mark it active.
  useEffect(() => {
    if (clips.length === 0) return;
    const observer = new IntersectionObserver(
      (entries) => {
        entries.forEach((entry) => {
          if (entry.isIntersecting && entry.intersectionRatio >= 0.6) {
            const idx = itemRefs.current.indexOf(
              entry.target as HTMLDivElement
            );
            if (idx !== -1) setActiveIndex(idx);
          }
        });
      },
      { threshold: [0.6] }
    );
    itemRefs.current.forEach((el) => el && observer.observe(el));
    return () => observer.disconnect();
  }, [clips]);

  if (loading) return <LoadingIndicator />;

  if (clips.length === 0) {
    return (
      <div className="clip-feed-empty">
        <p>No clips to show.</p>
        <Button onClick={() => history.push("/clips")}>Back to clips</Button>
      </div>
    );
  }

  return (
    <div className="clip-feed">
      <Button
        variant="minimal"
        className="clip-feed-back"
        onClick={() => history.push("/clips")}
        title="Back to clips"
      >
        <Icon icon={faArrowLeft} />
      </Button>
      {clips.map((clip, i) => (
        <ClipFeedItem
          key={clip.id}
          clip={clip}
          muted={muted}
          onToggleMute={() => setMuted((m) => !m)}
          active={i === activeIndex}
          registerRef={(el) => (itemRefs.current[i] = el)}
          onPlayed={onPlayed}
        />
      ))}
    </div>
  );
};

export default ClipFeed;
