package data

import "fmt"

type FileMetadata struct {
	Name         string
	Length       uint32
	Md5sum       *string
	Path         []string
	PieceIndexes []int
}

/**
 * Parses an info dictionary as if the dictionary for a file
 *
 * @param fileInfo The info dictionary for the file
 */
func parseFile(fileInfo map[string]interface{}) (FileMetadata, error) {
	result := FileMetadata{}

	// Parse the name and path if it exists
	if fileInfo["path"] == nil {
		if fileInfo["name"] == nil {
			return result, fmt.Errorf("File is missing the \"name\" field")
		}
		result.Name = fileInfo["name"].(string)
	} else {
		filePathArray := fileInfo["path"].([]interface{})
		result.Name = fmt.Sprintf(filePathArray[len(filePathArray)-1].(string))

		// Parse the path
		result.Path = make([]string, len(filePathArray))
		for index, value := range filePathArray {
			result.Path[index] = fmt.Sprintf(value.([]interface{})[0].(string))
		}
	}

	// Parse the length
	if fileInfo["length"] == nil {
		return result, fmt.Errorf("File is missing the \"length\" field")
	}
	result.Length = uint32(fileInfo["length"].(int64))

	// Parse the md5sum if there is one
	if fileInfo["md5sum"] != nil {
		result.Md5sum = new(string)
		*result.Md5sum = fileInfo["md5sum"].(string)
	}

	return result, nil
}
