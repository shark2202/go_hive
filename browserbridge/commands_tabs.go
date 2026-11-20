package browserbridge

import (
	"context"
	"encoding/json"
)

func cmdTabsList(ctx context.Context, b *Bridge, profile string, params map[string]any) (any, error) {
	groupID := 0
	send := map[string]any{}
	gidRaw, hasGID := params["group_id"]
	if !hasGID || gidRaw == nil {
		gidRaw, hasGID = params["groupId"]
	}
	if hasGID && gidRaw != nil {
		n, valid := toInt(gidRaw)
		if !valid {
			return nil, badRequest("'group_id' must be an integer")
		}
		groupID = n
		send["groupId"] = n
	}
	var conn *Connection
	var err error
	if groupID > 0 {
		conn, err = b.resolveForGroup(groupID, profile)
	} else {
		conn, err = b.resolve(profile, nil)
	}
	if err != nil {
		return nil, err
	}
	raw, err := conn.Call(ctx, "tab.list", send, b.cfg.CommandTimeout)
	if err != nil {
		return nil, err
	}
	var res struct {
		Tabs []struct {
			ID int `json:"id"`
		} `json:"tabs"`
	}
	if json.Unmarshal(raw, &res) == nil {
		ids := make([]int, 0, len(res.Tabs))
		for _, tab := range res.Tabs {
			if tab.ID > 0 {
				ids = append(ids, tab.ID)
			}
		}
		b.replaceTabs(conn.Label(), ids)
	}
	return rawResult(raw)
}

func cmdTabOpen(ctx context.Context, b *Bridge, profile string, params map[string]any) (any, error) {
	groupID := 0
	send := map[string]any{}
	url, _ := paramString(params, "url")
	if url == "" {
		url = "about:blank"
	}
	send["url"] = url
	if gid, ok := params["groupId"]; ok && gid != nil {
		n, valid := toInt(gid)
		if !valid {
			return nil, badRequest("'groupId' must be an integer")
		}
		groupID = n
		send["groupId"] = n
	}
	var conn *Connection
	var err error
	if groupID > 0 {
		conn, err = b.resolveForGroup(groupID, profile)
	} else {
		conn, err = b.resolve(profile, nil)
	}
	if err != nil {
		return nil, err
	}
	raw, err := conn.Call(ctx, "tab.create", send, b.cfg.CommandTimeout)
	if err != nil {
		return nil, err
	}
	res, err := resultMap(raw)
	if err != nil {
		return nil, err
	}
	if id, ok := toInt(res["tabId"]); ok && id > 0 {
		b.rememberTab(id, conn.Label())
		if groupID > 0 {
			if meta, ok := b.contexts.ByGroup(groupID); ok {
				b.setTabOwner(id, meta.Profile)
				b.addHiveTab(id)
			}
		}
		res["tabId"] = id
	}
	res["url"] = url
	return res, nil
}

func cmdTabClose(ctx context.Context, b *Bridge, profile string, params map[string]any) (any, error) {
	tabID, err := paramTabID(params)
	if err != nil {
		return nil, err
	}
	conn, err := b.resolve(profile, &tabID)
	if err != nil {
		return nil, err
	}
	raw, err := conn.Call(ctx, "tab.close", map[string]any{"tabId": tabID}, b.cfg.CommandTimeout)
	if err != nil {
		return nil, err
	}
	b.forgetTab(tabID)
	conn.forgetAttached(tabID)
	b.forgetTabState(tabID)
	b.clearTabHealth(tabID)
	b.clearPendingDialog(tabID)
	b.setTabOwner(tabID, "")
	res, err := resultMap(raw)
	if err != nil {
		return nil, err
	}
	res["tabId"] = tabID
	return res, nil
}
