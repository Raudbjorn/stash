import React, { useCallback, useEffect, useRef } from "react";

type PointerTiltHandlers<T extends HTMLElement> = Pick<
  React.HTMLAttributes<T>,
  "onPointerEnter" | "onPointerMove" | "onPointerLeave" | "onBlur"
>;

type PointerPosition = {
  x: number;
  y: number;
};

export function usePointerTilt<T extends HTMLElement>(
  enabled: boolean,
  maxDegrees = 4
): PointerTiltHandlers<T> {
  const targetRef = useRef<T | null>(null);
  const boundsRef = useRef<DOMRect | null>(null);
  const pointerRef = useRef<PointerPosition | null>(null);
  const animationFrameRef = useRef<number | null>(null);

  const reset = useCallback(() => {
    if (animationFrameRef.current !== null) {
      cancelAnimationFrame(animationFrameRef.current);
      animationFrameRef.current = null;
    }

    const target = targetRef.current;
    if (target) {
      target.style.setProperty("--performer-tilt-x", "0deg");
      target.style.setProperty("--performer-tilt-y", "0deg");
      target.style.setProperty("--performer-glow-x", "50%");
      target.style.setProperty("--performer-glow-y", "50%");
    }

    boundsRef.current = null;
    pointerRef.current = null;
  }, []);

  useEffect(() => {
    if (!enabled) reset();
    return reset;
  }, [enabled, reset]);

  const onPointerEnter = useCallback<
    NonNullable<PointerTiltHandlers<T>["onPointerEnter"]>
  >(
    (event) => {
      targetRef.current = event.currentTarget;
      if (!enabled) {
        reset();
        return;
      }

      boundsRef.current = event.currentTarget.getBoundingClientRect();
    },
    [enabled, reset]
  );

  const onPointerMove = useCallback<
    NonNullable<PointerTiltHandlers<T>["onPointerMove"]>
  >(
    (event) => {
      if (!enabled || !boundsRef.current) return;

      pointerRef.current = { x: event.clientX, y: event.clientY };
      if (animationFrameRef.current !== null) return;

      animationFrameRef.current = requestAnimationFrame(() => {
        animationFrameRef.current = null;
        const target = targetRef.current;
        const bounds = boundsRef.current;
        const pointer = pointerRef.current;
        if (!target || !bounds || !pointer) return;

        const x = Math.max(
          0,
          Math.min(1, (pointer.x - bounds.left) / bounds.width)
        );
        const y = Math.max(
          0,
          Math.min(1, (pointer.y - bounds.top) / bounds.height)
        );
        const tiltX = (0.5 - y) * maxDegrees * 2;
        const tiltY = (x - 0.5) * maxDegrees * 2;

        target.style.setProperty("--performer-tilt-x", `${tiltX}deg`);
        target.style.setProperty("--performer-tilt-y", `${tiltY}deg`);
        target.style.setProperty("--performer-glow-x", `${x * 100}%`);
        target.style.setProperty("--performer-glow-y", `${y * 100}%`);
      });
    },
    [enabled, maxDegrees]
  );

  const onPointerLeave = useCallback<
    NonNullable<PointerTiltHandlers<T>["onPointerLeave"]>
  >(() => reset(), [reset]);
  const onBlur = useCallback<NonNullable<PointerTiltHandlers<T>["onBlur"]>>(
    () => reset(),
    [reset]
  );

  return { onPointerEnter, onPointerMove, onPointerLeave, onBlur };
}
