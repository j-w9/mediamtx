package defs

import "time"

// APIPathDetectionObject is an object detected in the last analyzed frame.
type APIPathDetectionObject struct {
	Class string  `json:"class"`
	Score float64 `json:"score"`
	// position and size relative to the frame, between 0 and 1.
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

// APIPathDetectionSeen is the last time an object of a class was detected.
type APIPathDetectionSeen struct {
	Class string    `json:"class"`
	Time  time.Time `json:"time"`
}

// APIPathDetection is the state of object detection on a path.
type APIPathDetection struct {
	Model       string                   `json:"model"`
	Device      string                   `json:"device"`
	Classes     []string                 `json:"classes"`
	LastFrame   *time.Time               `json:"lastFrame"`
	InferenceMS float64                  `json:"inferenceMs"`
	FrameWidth  int                      `json:"frameWidth"`
	FrameHeight int                      `json:"frameHeight"`
	Objects     []APIPathDetectionObject `json:"objects"`
	Seen        []APIPathDetectionSeen   `json:"seen"`
	Error       string                   `json:"error"`
}

// APIDetectModel is a detection model.
type APIDetectModel struct {
	Name        string   `json:"name"`
	Classes     []string `json:"classes"`
	InputWidth  int      `json:"inputWidth"`
	InputHeight int      `json:"inputHeight"`
	Error       string   `json:"error"`
}

// APIDetectModelList is a list of detection models.
type APIDetectModelList struct {
	Items []APIDetectModel `json:"items"`
}
