import Mousetrap from "mousetrap";
import { useEffect, useRef } from "react";
import { RatingSystemType } from "src/utils/rating";

// Accept either the global Mousetrap singleton or a scoped instance (e.g. one
// created for the lightbox, where the global singleton is paused). Only bind and
// unbind are needed.
type MousetrapBinder = Pick<Mousetrap.MousetrapInstance, "bind" | "unbind">;

export function useRatingKeybinds(
  isVisible: boolean,
  ratingSystem: RatingSystemType | undefined,
  setRating: (v: number) => void,
  mousetrap: MousetrapBinder = Mousetrap
) {
  const firstChar = useRef<string | undefined>(undefined);
  const ratingTimeout = useRef<ReturnType<typeof setTimeout> | undefined>(
    undefined
  );

  const starRatingShortcuts: { [char: string]: number } = {
    "0": NaN,
    "1": 20,
    "2": 40,
    "3": 60,
    "4": 80,
    "5": 100,
  };

  // (re)start the window during which rating keys are bound. Clearing any
  // pending timeout first means a repeated "r" doesn't unbind mid-sequence.
  function restartRatingTimeout(unbind: () => void) {
    if (ratingTimeout.current) {
      clearTimeout(ratingTimeout.current);
    }
    ratingTimeout.current = setTimeout(() => {
      ratingTimeout.current = undefined;
      unbind();
    }, 1000);
  }

  function handleStarRatingKeybinds() {
    for (const key in starRatingShortcuts) {
      mousetrap.bind(key, () => setRating(starRatingShortcuts[key]));
    }

    restartRatingTimeout(() => {
      for (const key in starRatingShortcuts) {
        mousetrap.unbind(key);
      }
    });
  }

  function handleDecimalKeybinds() {
    firstChar.current = undefined;

    mousetrap.bind("`", () => {
      setRating(NaN);
    });

    for (let i = 0; i <= 9; ++i) {
      mousetrap.bind(i.toString(), () => {
        if (firstChar.current !== undefined) {
          let combined = parseInt(firstChar.current + i.toString(), 10);
          if (combined === 0) {
            combined = 100;
          }

          setRating(combined);
          firstChar.current = undefined;
        } else {
          firstChar.current = i.toString();
        }
      });
    }

    restartRatingTimeout(() => {
      firstChar.current = undefined;

      mousetrap.unbind("`");
      for (let i = 0; i <= 9; ++i) {
        mousetrap.unbind(i.toString());
      }
    });
  }

  useEffect(() => {
    if (!isVisible) return;

    mousetrap.bind("r", () => {
      // numeric keypresses get caught by jwplayer, so blur the element
      // if the rating sequence is started
      if (document.activeElement instanceof HTMLElement) {
        document.activeElement.blur();
      }

      if (!ratingSystem || ratingSystem === RatingSystemType.Stars) {
        return handleStarRatingKeybinds();
      } else {
        return handleDecimalKeybinds();
      }
    });

    return () => {
      mousetrap.unbind("r");
    };
  });
}
