FROM quay.io/keycloak/keycloak:26.8.0 AS keycloak
FROM eclipse-temurin:21-jdk AS provider
COPY --from=keycloak /opt/keycloak/lib/lib/main/ /keycloak-libs/
WORKDIR /build
COPY provider/ .
RUN mkdir classes && javac --release 21 -cp '/keycloak-libs/*' -d classes src/*.java test/*.java \
    && java -cp 'classes:/keycloak-libs/*' meterya.identity.FactorChoiceTest \
    && rm classes/meterya/identity/FactorChoiceTest*.class \
    && cp -r resources/* classes/ \
    && jar --create --file meterya-factor-choice.jar -C classes .
FROM keycloak
COPY --from=provider --chown=keycloak:keycloak /build/meterya-factor-choice.jar /opt/keycloak/providers/
