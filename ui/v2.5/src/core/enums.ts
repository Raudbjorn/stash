export enum ImageLightboxDisplayMode {
  Original = "ORIGINAL",
  FitXy = "FIT_XY",
  FitX = "FIT_X",
}

export enum ImageLightboxScrollMode {
  Zoom = "ZOOM",
  PanY = "PAN_Y",
}

export const imageLightboxDisplayModeIntlMap = new Map<
  ImageLightboxDisplayMode,
  string
>([
  [ImageLightboxDisplayMode.Original, "dialogs.lightbox.display_mode.original"],
  [
    ImageLightboxDisplayMode.FitXy,
    "dialogs.lightbox.display_mode.fit_to_screen",
  ],
  [
    ImageLightboxDisplayMode.FitX,
    "dialogs.lightbox.display_mode.fit_horizontally",
  ],
]);

export const imageLightboxScrollModeIntlMap = new Map<
  ImageLightboxScrollMode,
  string
>([
  [ImageLightboxScrollMode.Zoom, "dialogs.lightbox.scroll_mode.zoom"],
  [ImageLightboxScrollMode.PanY, "dialogs.lightbox.scroll_mode.pan_y"],
]);
