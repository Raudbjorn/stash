import React, { useEffect, useMemo, useState } from "react";
import * as GQL from "src/core/generated-graphql";
import { getPlatformURL } from "src/core/createClient";

type PerformerImageProps = Omit<
  React.ImgHTMLAttributes<HTMLImageElement>,
  "src" | "alt"
> & {
  performer: Pick<
    GQL.Performer,
    "id" | "name" | "disambiguation" | "image_path"
  >;
};

export const PerformerImage: React.FC<PerformerImageProps> = ({
  performer,
  loading = "lazy",
  onError,
  ...imageProps
}) => {
  const imagePath = performer.image_path ?? "";
  const imageKey = `${performer.id}\u0000${imagePath}`;
  const [failedImageKey, setFailedImageKey] = useState<string | null>(
    imagePath ? null : imageKey
  );

  useEffect(() => {
    setFailedImageKey(imagePath ? null : imageKey);
  }, [imageKey, imagePath]);

  const fallbackPath = useMemo(() => {
    const url = getPlatformURL(`performer/${performer.id}/image`);
    url.searchParams.set("default", "true");
    return url.toString();
  }, [performer.id]);

  const usingFallback = !imagePath || failedImageKey === imageKey;
  const accessibleName = performer.disambiguation
    ? `${performer.name ?? ""} (${performer.disambiguation})`
    : (performer.name ?? "");

  function handleError(event: React.SyntheticEvent<HTMLImageElement, Event>) {
    if (!usingFallback) {
      setFailedImageKey(imageKey);
      return;
    }

    onError?.(event);
  }

  return (
    <img
      {...imageProps}
      loading={loading}
      alt={accessibleName}
      src={usingFallback ? fallbackPath : imagePath}
      onError={handleError}
    />
  );
};
