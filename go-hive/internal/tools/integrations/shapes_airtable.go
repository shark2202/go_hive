package integrations

// Airtable's upstream tool projects every response: records are flattened to
// {id, fields, created_time}, list endpoints add count, and mutations report
// result/count. The provider's records[] envelope never reaches the model.
func airtableShapes() map[string]shapeFunc {
	return map[string]shapeFunc{
		"airtable_list_records":       airtableRecordsShape,
		"airtable_get_record":         airtableRecordShape,
		"airtable_create_records":     airtableMutationShape("created"),
		"airtable_update_records":     airtableMutationShape("updated"),
		"airtable_list_bases":         airtableBasesShape,
		"airtable_get_base_schema":    airtableSchemaShape,
		"airtable_delete_records":     airtableDeleteShape,
		"airtable_search_records":     airtableRecordsShape,
		"airtable_list_collaborators": airtableCollaboratorsShape,
	}
}

// airtableRecord is the flattened record shape shared by the read paths.
func airtableRecord(value any) map[string]any {
	record := shapeMap(value)
	return map[string]any{
		"id":           shapeText(record["id"]),
		"fields":       shapeDict(record, "fields"),
		"created_time": shapeText(record["createdTime"]),
	}
}

// airtableRecordSummary is the create/update projection, which omits
// created_time.
func airtableRecordSummary(value any) map[string]any {
	record := shapeMap(value)
	return map[string]any{
		"id":     shapeText(record["id"]),
		"fields": shapeDict(record, "fields"),
	}
}

// airtableRecordsShape mirrors airtable_list_records / airtable_search_records:
//
//	count = len(records); records = [{id, fields, created_time}]
func airtableRecordsShape(parsed any, _ map[string]any) any {
	object := shapeMap(parsed)
	records := shapeList(object, "records")
	out := make([]any, 0, len(records))
	for _, record := range records {
		out = append(out, airtableRecord(record))
	}
	projected := map[string]any{"count": len(out), "records": out}
	if offset, present := object["offset"]; present {
		projected["has_more"] = true
		projected["offset"] = shapeText(offset)
	}
	return projected
}

// airtableRecordShape mirrors airtable_get_record.
func airtableRecordShape(parsed any, _ map[string]any) any {
	return airtableRecord(parsed)
}

// airtableMutationShape mirrors create/update: {"result":..., "count":...,
// "records":[{id, fields}]}.
func airtableMutationShape(result string) shapeFunc {
	return func(parsed any, _ map[string]any) any {
		records := shapeList(shapeMap(parsed), "records")
		out := make([]any, 0, len(records))
		for _, record := range records {
			out = append(out, airtableRecordSummary(record))
		}
		return map[string]any{"result": result, "count": len(out), "records": out}
	}
}

// airtableBasesShape mirrors airtable_list_bases.
func airtableBasesShape(parsed any, _ map[string]any) any {
	bases := shapeList(shapeMap(parsed), "bases")
	out := make([]any, 0, len(bases))
	for _, value := range bases {
		base := shapeMap(value)
		out = append(out, map[string]any{
			"id":               shapeText(base["id"]),
			"name":             shapeText(base["name"]),
			"permission_level": shapeText(base["permissionLevel"]),
		})
	}
	return map[string]any{"count": len(out), "bases": out}
}

// airtableSchemaShape mirrors airtable_get_base_schema.
func airtableSchemaShape(parsed any, _ map[string]any) any {
	tables := shapeList(shapeMap(parsed), "tables")
	out := make([]any, 0, len(tables))
	for _, value := range tables {
		table := shapeMap(value)
		fields := shapeList(table, "fields")
		projected := make([]any, 0, len(fields))
		for _, rawField := range fields {
			field := shapeMap(rawField)
			projected = append(projected, map[string]any{
				"id":   shapeText(field["id"]),
				"name": shapeText(field["name"]),
				"type": shapeText(field["type"]),
			})
		}
		out = append(out, map[string]any{
			"id":     shapeText(table["id"]),
			"name":   shapeText(table["name"]),
			"fields": projected,
		})
	}
	return map[string]any{"count": len(out), "tables": out}
}

// airtableDeleteShape mirrors airtable_delete_records: count all returned
// records, but list only the ids the provider marked deleted.
func airtableDeleteShape(parsed any, _ map[string]any) any {
	records := shapeList(shapeMap(parsed), "records")
	deleted := make([]any, 0, len(records))
	for _, value := range records {
		record := shapeMap(value)
		if shapeBool(record, "deleted", false) {
			deleted = append(deleted, shapeText(record["id"]))
		}
	}
	return map[string]any{"result": "deleted", "count": len(records), "deleted_ids": deleted}
}

// airtableCollaboratorsShape mirrors airtable_list_collaborators.
func airtableCollaboratorsShape(parsed any, _ map[string]any) any {
	collaborators := shapeList(shapeMap(parsed), "collaborators")
	out := make([]any, 0, len(collaborators))
	for _, value := range collaborators {
		collaborator := shapeMap(value)
		out = append(out, map[string]any{
			"user_id":          shapeText(collaborator["userId"]),
			"email":            shapeText(collaborator["email"]),
			"permission_level": shapeText(collaborator["permissionLevel"]),
		})
	}
	return map[string]any{"count": len(out), "collaborators": out}
}
