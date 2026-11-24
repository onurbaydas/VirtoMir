package io.github.onurbaydas.virtomir;

/** An error answer of the VirtoMir API. */
public class VirtoMirException extends RuntimeException {
    private final int status;

    public VirtoMirException(int status, String message) {
        super("VirtoMir API " + status + ": " + message);
        this.status = status;
    }

    /** The HTTP status of the answer. */
    public int status() {
        return status;
    }

    public boolean isNotFound() {
        return status == 404;
    }
}
