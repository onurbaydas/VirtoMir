package io.github.onurbaydas.virtomir;

/** Power and lifecycle actions a machine accepts. */
public enum PowerAction {
    START("start"), SHUTDOWN("shutdown"), REBOOT("reboot"), FORCE_OFF("force-off"),
    PAUSE("pause"), RESUME("resume"), RESET("reset"), SAVE("save");

    private final String wire;

    PowerAction(String wire) {
        this.wire = wire;
    }

    /** The name the API uses. */
    public String wire() {
        return wire;
    }
}
