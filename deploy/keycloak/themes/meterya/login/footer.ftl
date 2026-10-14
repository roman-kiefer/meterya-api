<#macro content>
    <#if meteryaFactorChoice?? && meteryaFactorChoice>
        <form action="${url.loginAction}" method="post" class="meterya-factor-change">
            <button type="submit" name="factorMethod" value="choose">${msg("meteryaFactorChange")}</button>
        </form>
    </#if>
    <div class="meterya-brand" aria-label="meterya">
        <img src="${url.resourcesPath}/img/logo.svg" width="36" height="36" alt="" />
        <span>meterya</span>
    </div>
</#macro>
