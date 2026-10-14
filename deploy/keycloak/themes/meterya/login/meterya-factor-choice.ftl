<#import "template.ftl" as layout>
<@layout.registrationLayout displayMessage=true; section>
    <#if section == "header">
        ${msg("meteryaFactorTitle")}
    <#elseif section == "form">
        <p>${msg("meteryaFactorHelp")}</p>
        <form action="${url.loginAction}" method="post" class="meterya-factor-options">
            <button type="submit" name="factorMethod" value="webauthn-register" class="meterya-factor-option">
                <strong>${msg("meteryaFactorPasskey")}</strong>
                <span>${msg("meteryaFactorPasskeyHelp")}</span>
            </button>
            <button type="submit" name="factorMethod" value="CONFIGURE_TOTP" class="meterya-factor-option">
                <strong>${msg("meteryaFactorOtp")}</strong>
                <span>${msg("meteryaFactorOtpHelp")}</span>
            </button>
        </form>
    </#if>
</@layout.registrationLayout>
