package io.github.onurbaydas.virtomir;

import com.fasterxml.jackson.annotation.JsonIgnoreProperties;

/** A snapshot of a machine; created is in unix seconds. */
@JsonIgnoreProperties(ignoreUnknown = true)
public record Snapshot(String name, String description, String parent, String state, long created,
                       boolean current, boolean memory, String location) {
}
