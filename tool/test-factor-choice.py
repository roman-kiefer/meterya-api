"""Smoke-test native enrollment forms in a disposable realm, without SMTP or real credentials."""
import json
import subprocess
import uuid
import urllib.request
import urllib.parse
import http.cookiejar
from html.parser import HTMLParser

realm = 'meterya-factor-test-' + uuid.uuid4().hex
config = '/tmp/' + realm
base = 'http://localhost:8080/identity'

def admin(*args, data=None):
    result = subprocess.run(['docker', 'exec', '-i', 'meterya-keycloak-1',
        '/opt/keycloak/bin/kcadm.sh', *args, '--config', config],
        input=None if data is None else json.dumps(data).encode(), capture_output=True)
    if result.returncode:
        raise RuntimeError('Keycloak test fixture operation failed')
    return result.stdout

class Form(HTMLParser):
    def __init__(self, html):
        super().__init__()
        self.action = None
        self.inputs = {}
        self.feed(html)
    def handle_starttag(self, tag, attrs):
        attrs = dict(attrs)
        if tag == 'form' and self.action is None:
            self.action = attrs.get('action')
        if tag == 'input' and attrs.get('type') == 'hidden' and 'name' in attrs:
            self.inputs[attrs['name']] = attrs.get('value', '')

created = False
try:
    login = 'set -eu; /opt/keycloak/bin/kcadm.sh config credentials --config "$1" --server http://localhost:8080/identity --realm master --user "$KC_BOOTSTRAP_ADMIN_USERNAME" --password "$KC_BOOTSTRAP_ADMIN_PASSWORD" >/dev/null 2>&1'
    subprocess.run(['docker','exec','meterya-keycloak-1','sh','-c',login,'test',config], check=True, capture_output=True)
    admin('create', 'realms', '-f', '-', data={
        'realm': realm, 'enabled': True, 'loginTheme': 'meterya',
        'clients': [{'clientId': 'test', 'enabled': True, 'publicClient': True,
            'redirectUris': ['http://localhost:8080/test-complete'], 'standardFlowEnabled': True}],
        'users': [{'username': 'fixture', 'enabled': True, 'emailVerified': True,
            'email': 'fixture@example.test', 'firstName': 'Test', 'lastName': 'Fixture',
            'requiredActions': ['meterya-configure-factor'],
            'credentials': [{'type': 'password', 'value': 'Disposable-test-password-123!', 'temporary': False}]}]})
    created = True
    admin('create', 'authentication/register-required-action', '-r', realm,
        '-s', 'providerId=meterya-configure-factor', '-s', 'name=Choose factor')
    admin('update', 'authentication/required-actions/meterya-configure-factor', '-r', realm, '-s', 'enabled=true')
    admin('update', 'authentication/required-actions/webauthn-register', '-r', realm, '-s', 'enabled=true')

    class LocalTestCookies(http.cookiejar.DefaultCookiePolicy):
        def return_ok_secure(self, cookie, request):
            # Browsers treat localhost as trustworthy; urllib otherwise omits these cookies.
            return request.host == 'localhost:8080' or super().return_ok_secure(cookie, request)
    opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar(LocalTestCookies())))
    def get(url):
        with opener.open(url) as response: return response.read().decode()
    def post(html, fields):
        form = Form(html)
        assert form.action and 'localhost:8080/identity/' in form.action
        with opener.open(form.action, urllib.parse.urlencode(fields).encode()) as response:
            assert '/test-complete' not in response.url, 'Enrollment bypassed'
            return response.read().decode()

    query = urllib.parse.urlencode({'client_id':'test','redirect_uri':'http://localhost:8080/test-complete','response_type':'code','scope':'openid'})
    html = get(base + '/realms/' + realm + '/protocol/openid-connect/auth?' + query)
    html = post(html, {'username':'fixture','password':'Disposable-test-password-123!'})
    assert 'meterya-factor-options' in html and 'webauthn-register' in html and 'CONFIGURE_TOTP' in html
    html = post(html, {'factorMethod':'UPDATE_PASSWORD'})
    assert 'meterya-factor-options' in html, 'Unexpected method accepted'
    html = post(html, {'factorMethod':'CONFIGURE_TOTP'})
    assert 'totpSecret' in html and 'factorMethod' in html, 'Native OTP setup or switch missing'
    fields = Form(html).inputs
    fields['totp'] = 'invalid'
    html = post(html, fields)
    assert 'totpSecret' in html, 'Invalid OTP bypassed enrollment'
    html = post(html, {'factorMethod':'choose'})
    assert 'meterya-factor-options' in html
    html = post(html, {'factorMethod':'webauthn-register'})
    assert 'registerWebAuthn' in html and 'factorMethod' in html, 'Native passkey setup or switch missing'
    html = post(html, {'factorMethod':'choose'})
    assert 'meterya-factor-options' in html
    print('Keycloak smoke test passed: choice, rejected invalid method, OTP validation, passkey form and method switching.')
finally:
    if created:
        assert realm.startswith('meterya-factor-test-')
        admin('delete', 'realms/' + realm)
    subprocess.run(['docker','exec','meterya-keycloak-1','rm','-f',config], check=True, capture_output=True)
