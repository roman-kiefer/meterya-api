package meterya.identity;

import java.lang.reflect.Proxy;
import java.util.HashMap;
import java.util.Map;
import jakarta.ws.rs.core.MultivaluedHashMap;
import org.keycloak.authentication.RequiredActionContext;
import org.keycloak.authentication.RequiredActionProvider;
import org.keycloak.forms.login.LoginFormsProvider;
import org.keycloak.http.HttpRequest;
import org.keycloak.models.KeycloakSession;
import org.keycloak.models.RealmModel;
import org.keycloak.models.RequiredActionProviderModel;
import org.keycloak.sessions.AuthenticationSessionModel;

/** Dependency-free contract checks run against the exact deployed Keycloak SPI. */
public final class FactorChoiceTest {
    interface Handler { Object invoke(String name, Object[] args); }
    static <T> T fake(Class<T> type, Handler handler) {
        return type.cast(Proxy.newProxyInstance(type.getClassLoader(), new Class[]{type},
            (proxy, method, args) -> handler.invoke(method.getName(), args)));
    }
    static void check(boolean value) { if (!value) throw new AssertionError(); }

    public static void main(String[] args) {
        for (String method : new String[]{"CONFIGURE_TOTP", "webauthn-register"}) verify(method);
        check(!FactorChoice.allowed("UPDATE_PASSWORD"));
        check(!FactorChoice.allowed("CONFIGURE_RECOVERY_AUTHN_CODES"));
        check(!FactorChoice.allowed(null));
        System.out.println("Factor choice contract tests passed.");
    }

    static void verify(String method) {
        Map<String,String> notes = new HashMap<>();
        var input = new MultivaluedHashMap<String,String>();
        int[] successes = {0}, challenges = {0}, delegated = {0};
        boolean[] registered = {false}, enabled = {true};
        RequiredActionProvider nativeProvider = new RequiredActionProvider() {
            public void evaluateTriggers(RequiredActionContext c) { }
            public void requiredActionChallenge(RequiredActionContext c) { delegated[0]++; c.challenge(null); }
            public void processAction(RequiredActionContext c) { delegated[0]++; if (registered[0]) c.success(); else c.challenge(null); }
            public void close() { }
        };
        var session = fake(KeycloakSession.class, (name,a) -> {
            check(name.equals("getProvider") && a[1].equals(method)); return nativeProvider;
        });
        var realm = fake(RealmModel.class, (name,a) -> {
            var action = new RequiredActionProviderModel();
            action.setAlias((String)a[0]); action.setProviderId((String)a[0]); action.setEnabled(enabled[0]); return action;
        });
        var auth = fake(AuthenticationSessionModel.class, (name,a) -> {
            return switch(name) {
                case "getAuthNote" -> notes.get(a[0]);
                case "setAuthNote" -> notes.put((String)a[0], (String)a[1]);
                case "removeAuthNote" -> notes.remove(a[0]);
                default -> throw new AssertionError(name);
            };
        });
        LoginFormsProvider[] formReference = new LoginFormsProvider[1];
        formReference[0] = fake(LoginFormsProvider.class, (name,a) -> name.equals("createForm") ? null : formReference[0]);
        final var forms = formReference[0];
        var request = fake(HttpRequest.class, (name,a) -> input);
        var context = fake(RequiredActionContext.class, (name,a) -> {
            return switch(name) {
                case "getAuthenticationSession" -> auth;
                case "getHttpRequest" -> request;
                case "getRealm" -> realm;
                case "getSession" -> session;
                case "form" -> forms;
                case "challenge" -> { challenges[0]++; yield null; }
                case "success" -> { successes[0]++; yield null; }
                default -> throw new AssertionError(name);
            };
        });
        var choice = new FactorChoice();
        choice.requiredActionChallenge(context);
        check(challenges[0] == 1 && successes[0] == 0);
        input.putSingle("factorMethod", "UPDATE_PASSWORD");
        choice.processAction(context);
        check(delegated[0] == 0 && successes[0] == 0);
        enabled[0] = false;
        input.putSingle("factorMethod", method);
        choice.processAction(context);
        check(delegated[0] == 0 && notes.isEmpty());
        enabled[0] = true;
        choice.processAction(context);
        check(delegated[0] == 1 && successes[0] == 0);
        input.clear();
        choice.processAction(context);
        check(successes[0] == 0); // Failed native enrollment cannot bypass setup.
        input.putSingle("factorMethod", "choose");
        choice.processAction(context);
        check(notes.isEmpty() && successes[0] == 0);
        input.putSingle("factorMethod", method);
        choice.processAction(context);
        input.clear(); registered[0] = true;
        choice.processAction(context);
        check(successes[0] == 1);
    }
}
