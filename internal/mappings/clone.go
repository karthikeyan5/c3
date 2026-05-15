package mappings

// Clone returns a deep copy of mf. Used by the broker's copy-on-write
// mutation path so concurrent readers always see an immutable snapshot.
// All nested maps and slices are duplicated; nil sub-values stay nil.
//
// Performance note: mappings.json is bounded by user config size
// (channels, groups, topics, cwd-mappings — typically tens to hundreds
// of entries total). Deep-copying on every mutation is cheap relative
// to the work mutations already do (atomic file rewrite, etc.).
func (mf *MappingsFile) Clone() *MappingsFile {
	if mf == nil {
		return nil
	}
	out := &MappingsFile{
		SchemaVersion: mf.SchemaVersion,
	}
	if mf.Channels != nil {
		out.Channels = make(map[string]ChannelConfig, len(mf.Channels))
		for k, v := range mf.Channels {
			out.Channels[k] = cloneChannelConfig(v)
		}
	}
	if mf.Codex != nil {
		c := *mf.Codex
		out.Codex = &c
	}
	if mf.Mappings != nil {
		out.Mappings = make(map[string]Mapping, len(mf.Mappings))
		for k, v := range mf.Mappings {
			out.Mappings[k] = v
		}
	}
	if mf.Plugins != nil {
		out.Plugins = make(map[string]map[string]any, len(mf.Plugins))
		for k, v := range mf.Plugins {
			if v == nil {
				out.Plugins[k] = nil
				continue
			}
			inner := make(map[string]any, len(v))
			for k2, v2 := range v {
				inner[k2] = v2
			}
			out.Plugins[k] = inner
		}
	}
	return out
}

func cloneChannelConfig(cc ChannelConfig) ChannelConfig {
	out := cc
	if cc.Groups != nil {
		out.Groups = make(map[string]GroupConfig, len(cc.Groups))
		for k, v := range cc.Groups {
			out.Groups[k] = v
		}
	}
	if cc.Topics != nil {
		out.Topics = append([]Topic(nil), cc.Topics...)
	}
	return out
}
