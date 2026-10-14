package meterya.identity;

import org.keycloak.Config;
import org.keycloak.authentication.RequiredActionContext;
import org.keycloak.authentication.RequiredActionFactory;
import org.keycloak.authentication.RequiredActionProvider;
import org.keycloak.models.KeycloakSession;
import org.keycloak.models.KeycloakSessionFactory;
import org.keycloak.models.RequiredActionProviderModel;

/** Selects the enrollment UI; Keycloak's native providers validate and store credentials. */
public final class FactorChoice implements RequiredActionProvider, RequiredActionFactory {
    public static final String ID = "meterya-configure-factor";
    private static final String NOTE = ID + ":method";

    static boolean allowed(String method) {
        return "CONFIGURE_TOTP".equals(method) || "webauthn-register".equals(method);
    }

    @Override public void evaluateTriggers(RequiredActionContext context) { }

    @Override public void requiredActionChallenge(RequiredActionContext context) {
        String method = context.getAuthenticationSession().getAuthNote(NOTE);
        if (!allowed(method)) {
            choose(context, false);
            return;
        }
        RequiredActionProvider provider = provider(context, method);
        if (provider == null) { context.failure(); return; }
        context.form().setAttribute("meteryaFactorChoice", true);
        provider.requiredActionChallenge(context);
    }

    @Override public void processAction(RequiredActionContext context) {
        String choice = context.getHttpRequest().getDecodedFormParameters().getFirst("factorMethod");
        if (choice != null) {
            if ("choose".equals(choice)) {
                context.getAuthenticationSession().removeAuthNote(NOTE);
                choose(context, false);
            } else if (allowed(choice) && provider(context, choice) != null) {
                context.getAuthenticationSession().setAuthNote(NOTE, choice);
                requiredActionChallenge(context);
            } else {
                choose(context, true);
            }
            return; // Selecting a method never completes this required action.
        }
        String method = context.getAuthenticationSession().getAuthNote(NOTE);
        RequiredActionProvider provider = allowed(method) ? provider(context, method) : null;
        if (provider == null) { choose(context, true); return; }
        context.form().setAttribute("meteryaFactorChoice", true);
        // Only the native registration provider can mark enrollment successful.
        provider.processAction(context);
    }

    private RequiredActionProvider provider(RequiredActionContext context, String method) {
        RequiredActionProviderModel action = context.getRealm().getRequiredActionProviderByAlias(method);
        if (action == null || !action.isEnabled()) return null;
        return context.getSession().getProvider(RequiredActionProvider.class, action.getProviderId());
    }

    private void choose(RequiredActionContext context, boolean invalid) {
        var form = context.form().setAttribute("meteryaFactorChoice", false);
        if (invalid) form.setError("meteryaFactorInvalid");
        context.challenge(form.createForm("meterya-factor-choice.ftl"));
    }

    @Override public String getId() { return ID; }
    @Override public String getDisplayText() { return "Choose and configure second factor"; }
    @Override public RequiredActionProvider create(KeycloakSession session) { return this; }
    @Override public void init(Config.Scope config) { }
    @Override public void postInit(KeycloakSessionFactory factory) { }
    @Override public void close() { }
}
