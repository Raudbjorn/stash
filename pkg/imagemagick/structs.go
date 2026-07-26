package imagemagick

// IMProbeJSONEntry represents a single image entry from ImageMagick's
// "json:" output format.
type IMProbeJSONEntry struct {
	Version string `json:"version"`
	Image   struct {
		Name              string `json:"name"`
		Format            string `json:"format"`
		FormatDescription string `json:"formatDescription"`
		MimeType          string `json:"mimeType"`
		Class             string `json:"class"`
		Geometry          struct {
			Width  int `json:"width"`
			Height int `json:"height"`
			X      int `json:"x"`
			Y      int `json:"y"`
		} `json:"geometry"`
		Units        string `json:"units"`
		Type         string `json:"type"`
		Endianness   string `json:"endianness"`
		Colorspace   string `json:"colorspace"`
		Depth        int    `json:"depth"`
		BaseDepth    int    `json:"baseDepth"`
		ChannelDepth struct {
			Red   int `json:"red"`
			Green int `json:"green"`
			Blue  int `json:"blue"`
		} `json:"channelDepth"`
		Pixels          int `json:"pixels"`
		ImageStatistics struct {
			All struct {
				Min               int     `json:"min"`
				Max               int     `json:"max"`
				Mean              float64 `json:"mean"`
				StandardDeviation float64 `json:"standardDeviation"`
				Kurtosis          float64 `json:"kurtosis"`
				Skewness          float64 `json:"skewness"`
				Entropy           float64 `json:"entropy"`
			} `json:"all"`
		} `json:"imageStatistics"`
		ChannelStatistics struct {
			Red struct {
				Min               int     `json:"min"`
				Max               int     `json:"max"`
				Mean              float64 `json:"mean"`
				StandardDeviation float64 `json:"standardDeviation"`
				Kurtosis          float64 `json:"kurtosis"`
				Skewness          float64 `json:"skewness"`
				Entropy           float64 `json:"entropy"`
			} `json:"red"`
			Green struct {
				Min               int     `json:"min"`
				Max               int     `json:"max"`
				Mean              float64 `json:"mean"`
				StandardDeviation float64 `json:"standardDeviation"`
				Kurtosis          float64 `json:"kurtosis"`
				Skewness          float64 `json:"skewness"`
				Entropy           float64 `json:"entropy"`
			} `json:"green"`
			Blue struct {
				Min               int     `json:"min"`
				Max               int     `json:"max"`
				Mean              float64 `json:"mean"`
				StandardDeviation float64 `json:"standardDeviation"`
				Kurtosis          float64 `json:"kurtosis"`
				Skewness          float64 `json:"skewness"`
				Entropy           float64 `json:"entropy"`
			} `json:"blue"`
		} `json:"channelStatistics"`
		RenderingIntent string  `json:"renderingIntent"`
		Gamma           float64 `json:"gamma"`
		Chromaticity    struct {
			RedPrimary struct {
				X float64 `json:"x"`
				Y float64 `json:"y"`
			} `json:"redPrimary"`
			GreenPrimary struct {
				X float64 `json:"x"`
				Y float64 `json:"y"`
			} `json:"greenPrimary"`
			BluePrimary struct {
				X float64 `json:"x"`
				Y float64 `json:"y"`
			} `json:"bluePrimary"`
			WhitePrimary struct {
				X float64 `json:"x"`
				Y float64 `json:"y"`
			} `json:"whitePrimary"`
		} `json:"chromaticity"`
		BackgroundColor  string `json:"backgroundColor"`
		BorderColor      string `json:"borderColor"`
		MatteColor       string `json:"matteColor"`
		TransparentColor string `json:"transparentColor"`
		Interlace        string `json:"interlace"`
		Intensity        string `json:"intensity"`
		Compose          string `json:"compose"`
		PageGeometry     struct {
			Width  int `json:"width"`
			Height int `json:"height"`
			X      int `json:"x"`
			Y      int `json:"y"`
		} `json:"pageGeometry"`
		Dispose     string `json:"dispose"`
		Iterations  int    `json:"iterations"`
		Compression string `json:"compression"`
		Orientation string `json:"orientation"`
		Properties  struct {
			DateCreate string `json:"date:create"`
			DateModify string `json:"date:modify"`
			Signature  string `json:"signature"`
		} `json:"properties"`
		Artifacts struct {
			Filename string `json:"filename"`
		} `json:"artifacts"`
		Tainted         bool   `json:"tainted"`
		Filesize        string `json:"filesize"`
		NumberPixels    string `json:"numberPixels"`
		PixelsPerSecond string `json:"pixelsPerSecond"`
		UserTime        string `json:"userTime"`
		ElapsedTime     string `json:"elapsedTime"`
		Version         string `json:"version"`
	} `json:"image"`
}

// IMProbeJSON is the top-level array returned by ImageMagick's "json:" output.
type IMProbeJSON []IMProbeJSONEntry
