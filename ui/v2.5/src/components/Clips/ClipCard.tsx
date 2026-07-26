import React from "react";
import * as GQL from "src/core/generated-graphql";
import TextUtils from "src/utils/text";
import { GridCard } from "../Shared/GridCard/GridCard";
import { RatingBanner } from "../Shared/RatingBanner";
import { OCounterButton } from "../Shared/CountButton";

interface IClipCardProps {
  clip: GQL.SlimClipDataFragment;
  cardWidth?: number;
  zoomIndex?: number;
}

export const ClipCard: React.FC<IClipCardProps> = ({
  clip,
  cardWidth,
  zoomIndex,
}) => {
  const title = clip.title || "Untitled clip";

  const duration =
    clip.end_seconds != null
      ? TextUtils.secondsToTimestamp(clip.end_seconds - clip.seconds)
      : "";

  function maybeRenderOCounter() {
    if (!clip.o_counter) return;
    return <OCounterButton value={clip.o_counter} />;
  }

  return (
    <GridCard
      className={`clip-card zoom-${zoomIndex}`}
      url={`/clips/${clip.id}`}
      width={cardWidth}
      title={title}
      linkClassName="clip-card-header"
      image={
        <>
          <video
            className="clip-card-video"
            src={clip.preview || undefined}
            muted
            preload="metadata"
            playsInline
          />
          {duration && <span className="clip-duration-badge">{duration}</span>}
          <RatingBanner rating={clip.rating100} />
        </>
      }
      details={
        <div className="clip-card__details">{maybeRenderOCounter()}</div>
      }
    />
  );
};
