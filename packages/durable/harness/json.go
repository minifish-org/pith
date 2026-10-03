package harness

// assignJSON assigns value at target[key] leaf by leaf. Chord records a
// container assignment as one full set and only emits an append when a string
// leaf is reassigned with a longer string, so writing the partial whole would
// store and publish the complete message on every flush.
func assignJSON(target map[string]any, key string, value any) {
	current := target[key]
	currentObject, currentIsObject := current.(map[string]any)
	valueObject, valueIsObject := value.(map[string]any)
	if currentIsObject && valueIsObject {
		for name := range currentObject {
			if _, ok := valueObject[name]; !ok {
				delete(currentObject, name)
			}
		}
		for name, child := range valueObject {
			assignJSON(currentObject, name, child)
		}
		return
	}
	currentArray, currentIsArray := current.([]any)
	valueArray, valueIsArray := value.([]any)
	if currentIsArray && valueIsArray && len(currentArray) <= len(valueArray) {
		for index := range valueArray {
			if index < len(currentArray) {
				if childObject, ok := currentArray[index].(map[string]any); ok {
					if nextObject, ok := valueArray[index].(map[string]any); ok {
						for name := range childObject {
							if _, ok := nextObject[name]; !ok {
								delete(childObject, name)
							}
						}
						for name, grandchild := range nextObject {
							assignJSON(childObject, name, grandchild)
						}
						continue
					}
				}
				currentArray[index] = valueArray[index]
			} else {
				currentArray = append(currentArray, valueArray[index])
			}
		}
		target[key] = currentArray
		return
	}
	if !jsonEqual(current, value) {
		target[key] = value
	}
}
