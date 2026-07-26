import React from "react";
import * as GQL from "src/core/generated-graphql";
import { ClipCard } from "./ClipCard";
import {
  useCardWidth,
  useContainerDimensions,
} from "../Shared/GridCard/GridCard";

interface IClipCardGrid {
  clips: GQL.SlimClipDataFragment[];
  zoomIndex?: number;
}

const zoomWidths = [210, 250, 300, 375];

export const ClipCardGrid: React.FC<IClipCardGrid> = ({
  clips,
  zoomIndex = 1,
}) => {
  const [componentRef, { width: containerWidth }] = useContainerDimensions();
  const cardWidth = useCardWidth(containerWidth, zoomIndex, zoomWidths);

  return (
    <div className="row justify-content-center" ref={componentRef}>
      {clips.map((clip) => (
        <ClipCard
          key={clip.id}
          clip={clip}
          cardWidth={cardWidth}
          zoomIndex={zoomIndex}
        />
      ))}
    </div>
  );
};
