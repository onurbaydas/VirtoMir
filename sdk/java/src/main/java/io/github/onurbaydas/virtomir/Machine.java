package io.github.onurbaydas.virtomir;

import com.fasterxml.jackson.annotation.JsonIgnoreProperties;

/** A machine as the list shows it. */
@JsonIgnoreProperties(ignoreUnknown = true)
public record Machine(String uuid, String name, String state, int vcpus, long maxMemoryKiB, boolean autostart, boolean template) {
    /** Whether the state counts as on (running, paused, …). */
    public boolean isRunning() {
        return state != null && !state.isEmpty()
                && !state.equals("shutoff") && !state.equals("crashed") && !state.equals("pmsuspended");
    }
}
