package tracker

// Parity: framework/host/colony_binding.py::ColonyBinding.from_dict
func BindingFromInputData(inputData map[string]any) *Binding {
	if inputData == nil {
		return nil
	}
	raw, ok := inputData["binding"]
	if !ok {
		return nil
	}
	switch value := raw.(type) {
	case nil:
		return nil
	case *Binding:
		if value == nil || value.Name == "" {
			return nil
		}
		copied := *value
		return &copied
	case Binding:
		if value.Name == "" {
			return nil
		}
		return &value
	case map[string]string:
		name := value["name"]
		dir, hasDir := value["dir"]
		trackerDB, hasTrackerDB := value["tracker_db"]
		if name == "" || !hasDir || !hasTrackerDB {
			return nil
		}
		return &Binding{Name: name, Dir: dir, TrackerDB: trackerDB}
	case map[string]any:
		name, ok := value["name"].(string)
		if !ok || name == "" {
			return nil
		}
		dir, ok := value["dir"].(string)
		if !ok {
			return nil
		}
		trackerDB, ok := value["tracker_db"].(string)
		if !ok {
			return nil
		}
		return &Binding{Name: name, Dir: dir, TrackerDB: trackerDB}
	default:
		return nil
	}
}
